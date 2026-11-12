package mattermost

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const fileLinkTTL = 24 * time.Hour

var ErrFileLinkUnavailable = errors.New("mattermost: file link expired or unavailable; request a fresh link from the bot")

type fileLinkQueries interface {
	GetChannelInstallationByAppID(context.Context, db.GetChannelInstallationByAppIDParams) (db.ChannelInstallation, error)
	GetChannelInstallationInWorkspace(context.Context, db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error)
}

// FileLinks grants temporary read access to one referenced file, without exposing bot credentials.
type FileLinks struct {
	q          fileLinkQueries
	box        *secretbox.Box
	decrypt    Decrypter
	policy     *ServerPolicy
	baseURL    string
	httpClient *http.Client
}

func NewFileLinks(q *db.Queries, key []byte, decrypt Decrypter, policy *ServerPolicy, baseURL string) (*FileLinks, error) {
	if len(key) != secretbox.KeySize {
		return nil, secretbox.ErrInvalidKey
	}
	base, err := canonicalServerURL(baseURL)
	if err != nil {
		return nil, errors.New("mattermost: file links require an absolute MULTICA_PUBLIC_URL")
	}
	// Separate link capabilities from encrypted installation credentials.
	linkKey := sha256.Sum256(append([]byte("mattermost-file-links-v1:"), key...))
	box, err := secretbox.New(linkKey[:])
	if err != nil {
		return nil, err
	}
	return &FileLinks{q: q, box: box, decrypt: decrypt, policy: policy, baseURL: base, httpClient: newHTTPClient(5 * time.Minute)}, nil
}

type fileTicket struct {
	Installation [16]byte `json:"installation"`
	Workspace    [16]byte `json:"workspace"`
	Config       [32]byte `json:"config"`
	ChannelID    string   `json:"channel"`
	PostID       string   `json:"post"`
	FileID       string   `json:"file"`
	Expires      int64    `json:"expires"`
}

func fileConfigDigest(cfg installConfig) [32]byte {
	encoded, _ := json.Marshal(cfg)
	return sha256.Sum256(encoded)
}

func (s *FileLinks) issue(row db.ChannelInstallation, cfg installConfig, post Post, fileID string) (string, error) {
	var current installConfig
	if !row.ID.Valid || !row.WorkspaceID.Valid || row.Status != "active" || json.Unmarshal(row.Config, &current) != nil || fileConfigDigest(current) != fileConfigDigest(cfg) ||
		cfg.AppID == "" || !validMattermostID(post.ID) || !validMattermostID(post.ChannelID) || !validMattermostID(fileID) {
		return "", ErrFileLinkUnavailable
	}
	encoded, err := json.Marshal(fileTicket{
		Installation: row.ID.Bytes, Workspace: row.WorkspaceID.Bytes, Config: fileConfigDigest(cfg), ChannelID: post.ChannelID,
		PostID: post.ID, FileID: fileID, Expires: time.Now().Add(fileLinkTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	sealed, err := s.box.Seal(encoded)
	if err != nil {
		return "", err
	}
	return s.baseURL + "/api/mattermost/files/download?ticket=" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open rechecks revocation and Mattermost access before fetching any file bytes.
func (s *FileLinks) Open(ctx context.Context, ticket, byteRange string) (*http.Response, FileInfo, error) {
	var grant fileTicket
	if len(ticket) > 4096 {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	sealed, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	plain, err := s.box.Open(sealed)
	if err != nil || json.Unmarshal(plain, &grant) != nil ||
		grant.Expires <= time.Now().Unix() || grant.Expires > time.Now().Add(fileLinkTTL).Unix() ||
		!validMattermostID(grant.PostID) || !validMattermostID(grant.ChannelID) || !validMattermostID(grant.FileID) {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	row, err := s.q.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID: pgtype.UUID{Bytes: grant.Installation, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: grant.Workspace, Valid: true}, ChannelType: string(TypeMattermost),
	})
	if err != nil || row.Status != "active" || !row.ID.Valid || !row.WorkspaceID.Valid || row.ID.Bytes != grant.Installation || row.WorkspaceID.Bytes != grant.Workspace {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	var cfg installConfig
	if json.Unmarshal(row.Config, &cfg) != nil || fileConfigDigest(cfg) != grant.Config {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	mode, err := cfg.AttachmentMode.normalized()
	if err != nil || mode != AttachmentsLinks {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	creds, err := decodeCredentials(row.Config, s.decrypt)
	if err != nil {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	base, err := s.policy.validate(creds.ServerURL)
	if err != nil {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	rest := newRESTClient(base, creds.AccessToken, s.httpClient)
	post, err := rest.GetPost(ctx, grant.PostID)
	if err != nil || post.ID != grant.PostID || post.ChannelID != grant.ChannelID || post.DeleteAt != 0 || !slices.Contains(post.FileIDs, grant.FileID) {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	info, err := rest.fileInfo(ctx, grant.PostID, grant.FileID)
	if err != nil {
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+apiPath+"/files/"+url.PathEscape(grant.FileID), nil)
	if err != nil {
		return nil, FileInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	response, err := rest.client.Do(req)
	if err != nil {
		return nil, FileInfo{}, &requestError{method: http.MethodGet, cause: err}
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		response.Body.Close()
		return nil, FileInfo{}, ErrFileLinkUnavailable
	}
	return response, info, nil
}

func validMattermostID(id string) bool {
	return len(id) == 26 && strings.IndexFunc(id, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) == -1
}
