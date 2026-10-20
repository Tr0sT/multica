package mattermost

import (
	"encoding/json"
	"errors"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type AccessMode string

const (
	AccessMembers AccessMode = "members"
	AccessAnyone  AccessMode = "anyone"
)

var ErrInvalidAccessMode = errors.New("mattermost: access_mode must be members or anyone")

func (mode AccessMode) normalized() (AccessMode, error) {
	switch mode {
	case "", AccessMembers:
		return AccessMembers, nil
	case AccessAnyone:
		return AccessAnyone, nil
	default:
		return "", ErrInvalidAccessMode
	}
}

func installationAccessMode(inst engine.ResolvedInstallation) (AccessMode, error) {
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return "", errors.New("mattermost: installation platform row unavailable")
	}
	var config installConfig
	if err := json.Unmarshal(row.Config, &config); err != nil {
		return "", err
	}
	return config.AccessMode.normalized()
}

func sessionKeyForAccess(inst engine.ResolvedInstallation, key string) (string, error) {
	mode, err := installationAccessMode(inst)
	if err != nil {
		return "", err
	}
	if mode == AccessAnyone {
		// Delegated runs must not resume a linked member's private agent context.
		return "public:" + inst.InstallerUserID.String() + ":" + key, nil
	}
	return key, nil
}
