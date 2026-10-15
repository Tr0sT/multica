package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This file is the minimal Mattermost REST surface the adapter needs:
// GET /users/me (install-time validation and the bot identity),
// GET /posts/{id} (thread-root authorship for group addressing),
// POST /posts (outbound), PUT /posts/{id} (reserved for a future streaming
// edit). It is a thin hand-rolled client over net/http — the Mattermost API is
// plain JSON-over-HTTP with a bearer token, so no SDK dependency is warranted.

const (
	// apiPath is the versioned REST prefix, relative to the installation's
	// server URL (which may itself carry a sub-path).
	apiPath = "/api/v4"
	// websocketPath is the event-stream endpoint, same prefix.
	websocketPath = apiPath + "/websocket"
)

// requestError deliberately omits the request URL from Error(). Mattermost
// URLs carry no credential, but net/http transport errors can quote the whole
// request, and keeping the shape identical to the Telegram client means the
// next reader does not have to work out whether this one is the safe variant.
// Unwrap preserves cancellation and transport classification.
type requestError struct {
	method string
	cause  error
}

func (e *requestError) Error() string { return fmt.Sprintf("mattermost: %s request failed", e.method) }

func (e *requestError) Unwrap() error { return e.cause }

// apiError is a non-2xx Mattermost API response. Mattermost returns a JSON
// body with id / message / status_code on every error it generates itself; a
// proxy in front of it may not, so Message falls back to the raw body.
type apiError struct {
	StatusCode int
	ID         string
	Message    string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("mattermost api: http %d", e.StatusCode)
	}
	return fmt.Sprintf("mattermost api: http %d: %s", e.StatusCode, e.Message)
}

// statusOf reports the HTTP status an error carries, or 0 when it is not an
// API-level failure.
func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.StatusCode
	}
	return 0
}

// defaultTimeout bounds an ordinary REST call. The WebSocket dial and read
// loop do NOT use this client.
const defaultTimeout = 15 * time.Second

// newHTTPClient builds the REST client. Redirects are refused rather than
// followed: the server URL is operator-supplied and may be an internal host, so
// a redirect is the one way a compromised or misconfigured deployment could
// bounce the bearer token to a destination the admin never named.
func newHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// restClient is one installation's REST client.
type restClient struct {
	base   string // canonical server URL, no trailing slash
	token  string
	client *http.Client
}

func newRESTClient(base, token string, client *http.Client) *restClient {
	if client == nil {
		client = newHTTPClient(defaultTimeout)
	}
	return &restClient{base: strings.TrimRight(base, "/"), token: token, client: client}
}

// do issues one API call and decodes the response into out (skipped when out
// is nil). Non-2xx responses return *apiError.
func (c *restClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("mattermost: encode %s %s body: %w", method, path, err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+apiPath+path, reader)
	if err != nil {
		return fmt.Errorf("mattermost: build %s %s request: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return &requestError{method: method, cause: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeAPIError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("mattermost: decode %s %s response: %w", method, path, err)
	}
	return nil
}

const (
	// maxResponseBytes bounds a decoded API response. A Mattermost post is
	// capped well below this; the limit exists so a misrouted request to some
	// other service cannot stream unbounded data into the decoder.
	maxResponseBytes = 4 << 20
	// maxDrainBytes bounds the read-to-EOF that lets the connection be reused.
	maxDrainBytes = 64 << 10
	// maxErrorBodyBytes bounds the error body quoted back to the operator.
	maxErrorBodyBytes = 8 << 10
)

func decodeAPIError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	out := &apiError{StatusCode: resp.StatusCode}
	var parsed struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Message != "" {
		out.ID = parsed.ID
		out.Message = parsed.Message
		return out
	}
	out.Message = strings.TrimSpace(string(raw))
	return out
}

// User is the subset of the Mattermost user object the adapter reads.
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Nickname  string `json:"nickname,omitempty"`
	DeleteAt  int64  `json:"delete_at,omitempty"`
}

// Post is the subset of the Mattermost post object the adapter reads and
// writes. RootID is the thread root ("" for a top-level post); Mattermost
// collapses the older parent_id onto it.
type Post struct {
	ID            string         `json:"id,omitempty"`
	CreateAt      int64          `json:"create_at,omitempty"`
	UpdateAt      int64          `json:"update_at,omitempty"`
	DeleteAt      int64          `json:"delete_at,omitempty"`
	UserID        string         `json:"user_id,omitempty"`
	ChannelID     string         `json:"channel_id"`
	RootID        string         `json:"root_id,omitempty"`
	Message       string         `json:"message"`
	Type          string         `json:"type,omitempty"`
	FileIDs       []string       `json:"file_ids,omitempty"`
	Props         map[string]any `json:"props,omitempty"`
	PendingPostID string         `json:"pending_post_id,omitempty"`
}

type postList struct {
	Order                     []string        `json:"order"`
	Posts                     map[string]Post `json:"posts"`
	HasNext                   *bool           `json:"has_next,omitempty"`
	FirstInaccessiblePostTime int64           `json:"first_inaccessible_post_time,omitempty"`
}

// FindReply reconciles a lost POST response without sending another message.
// Only an exact marker authored by this bot in this channel is evidence.
func (c *restClient) FindReply(ctx context.Context, channelID, botID, marker string, since int64) (Post, bool, error) {
	before := ""
	// shortcut: scan at most 4000 posts; use a provider-side indexed lookup if busy rooms exceed this window.
	for page := 0; page < 20; page++ {
		query := url.Values{"per_page": {"200"}}
		if before != "" {
			query.Set("before", before)
		}
		var list postList
		if err := c.do(ctx, http.MethodGet, "/channels/"+url.PathEscape(channelID)+"/posts?"+query.Encode(), nil, &list); err != nil {
			return Post{}, false, err
		}
		if len(list.Order) == 0 {
			return Post{}, false, nil
		}
		older := false
		for _, id := range list.Order {
			post, ok := list.Posts[id]
			if !ok {
				return Post{}, false, errors.New("mattermost: incomplete post list")
			}
			if post.ID != "" && post.UserID == botID && post.ChannelID == channelID && post.Props["multica_reply_id"] == marker {
				return post, true, nil
			}
			older = older || (post.CreateAt > 0 && post.CreateAt < since)
		}
		if older || len(list.Order) < 200 {
			return Post{}, false, nil
		}
		next := list.Order[len(list.Order)-1]
		if next == before {
			return Post{}, false, errors.New("mattermost: post pagination did not advance")
		}
		before = next
	}
	return Post{}, false, errors.New("mattermost: reply reconciliation exceeded its page limit")
}

// GetMe validates the token and returns the authenticated bot's identity.
func (c *restClient) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.do(ctx, http.MethodGet, "/users/me", nil, &u)
	return u, err
}

func (c *restClient) GetUser(ctx context.Context, userID string) (User, error) {
	var u User
	err := c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(userID), nil, &u)
	return u, err
}

// GetPost reads one post. The adapter uses it only to learn who authored a
// thread root, so group addressing can recognize a reply into a thread the bot
// itself started.
func (c *restClient) GetPost(ctx context.Context, postID string) (Post, error) {
	var p Post
	err := c.do(ctx, http.MethodGet, "/posts/"+url.PathEscape(postID), nil, &p)
	return p, err
}

// CreatePost publishes a message. RootID threads it under an existing post.
func (c *restClient) CreatePost(ctx context.Context, p Post) (Post, error) {
	var created Post
	err := c.do(ctx, http.MethodPost, "/posts", p, &created)
	return created, err
}

// UpdatePost replaces a post's text. Not called in v1 — it is the primitive a
// future streaming reply would edit — but kept here so the REST surface is
// complete and testable in one place.
func (c *restClient) UpdatePost(ctx context.Context, postID, message string) (Post, error) {
	var updated Post
	err := c.do(ctx, http.MethodPut, "/posts/"+url.PathEscape(postID), map[string]string{
		"id":      postID,
		"message": message,
	}, &updated)
	return updated, err
}
