package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type AccessMode string

const (
	AccessMembers   AccessMode = "members"
	AccessAnyone    AccessMode = "anyone"
	AccessAllowlist AccessMode = "allowlist"
)

var (
	ErrInvalidAccessMode        = errors.New("mattermost: access_mode must be members, anyone or allowlist")
	ErrInvalidAllowedUsers      = errors.New("mattermost: provide up to 100 valid Mattermost usernames with access_mode allowlist")
	ErrAllowedUsersUnverifiable = errors.New("mattermost: could not verify the allowed accounts; check the bot's connection and user lookup permissions, then retry; nothing was saved")
	mattermostUsername          = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	mattermostUserID            = regexp.MustCompile(`^[a-z0-9]{26}$`)
)

type AllowedUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func (mode AccessMode) normalized() (AccessMode, error) {
	switch mode {
	case "", AccessMembers:
		return AccessMembers, nil
	case AccessAnyone, AccessAllowlist:
		return mode, nil
	default:
		return "", ErrInvalidAccessMode
	}
}

func installationAccess(inst engine.ResolvedInstallation) (installConfig, error) {
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return installConfig{}, errors.New("mattermost: installation platform row unavailable")
	}
	var config installConfig
	if err := json.Unmarshal(row.Config, &config); err != nil {
		return installConfig{}, err
	}
	mode, err := config.AccessMode.normalized()
	config.AccessMode = mode
	return config, err
}

func sessionKeyForAccess(inst engine.ResolvedInstallation, key string) (string, error) {
	config, err := installationAccess(inst)
	if err != nil {
		return "", err
	}
	if config.AccessMode == AccessAnyone {
		// Delegated runs must not resume a linked member's private agent context.
		return "public:" + inst.InstallerUserID.String() + ":" + key, nil
	}
	if config.AccessMode == AccessAllowlist {
		return "allowlist:" + inst.InstallerUserID.String() + ":" + key, nil
	}
	return key, nil
}

func normalizeAllowedUsernames(mode AccessMode, usernames []string) ([]string, error) {
	if mode != AccessAllowlist {
		if usernames != nil {
			return nil, ErrInvalidAllowedUsers
		}
		return nil, nil
	}
	if usernames == nil || len(usernames) > 100 {
		return nil, ErrInvalidAllowedUsers
	}
	names := make([]string, 0, len(usernames))
	seen := make(map[string]bool, len(usernames))
	for _, name := range usernames {
		name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "@"))
		if !mattermostUsername.MatchString(name) {
			return nil, ErrInvalidAllowedUsers
		}
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names, nil
}

func (c *restClient) resolveAllowedUsers(ctx context.Context, names []string, previous []AllowedUser) ([]AllowedUser, error) {
	byName := make(map[string]AllowedUser, len(previous))
	for _, user := range previous {
		// Keep grants attached to accounts when usernames are renamed or reused.
		if mattermostUserID.MatchString(user.ID) {
			byName[user.Username] = user
		}
	}
	var missing []string
	for _, name := range names {
		if _, ok := byName[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		var users []User
		if err := c.do(ctx, http.MethodPost, "/users/usernames", missing, &users); err != nil {
			return nil, ErrAllowedUsersUnverifiable
		}
		for _, user := range users {
			if user.IsBot || user.DeleteAt != 0 || !mattermostUserID.MatchString(user.ID) || !mattermostUsername.MatchString(user.Username) {
				return nil, ErrInvalidAllowedUsers
			}
			if _, duplicate := byName[user.Username]; duplicate {
				return nil, ErrAllowedUsersUnverifiable
			}
			byName[user.Username] = AllowedUser{ID: user.ID, Username: user.Username}
		}
	}
	allowed := make([]AllowedUser, 0, len(names))
	for _, name := range names {
		user, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w: @%s was not found or is not visible to the bot", ErrInvalidAllowedUsers, name)
		}
		allowed = append(allowed, user)
	}
	return allowed, nil
}
