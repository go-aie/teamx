package room

import (
	"github.com/go-aie/teamx/internal/platform/notification"
)

// The source receives snapshots through this adapter, never the manager itself.
// Calls occur synchronously under the existing manager admission lock.
type notificationRuntimeHost struct {
	manager *roomManager
	source  notification.SourceID
}

func (h notificationRuntimeHost) Account(owner RoomIdentity, id string) (notification.Account, error) {
	a, _, err := h.manager.resolvePlatformAccountLocked(owner, id, h.source.Provider)
	if err != nil {
		return notification.Account{}, err
	}
	result := notification.Account{PlatformID: a.PlatformID, Generation: a.Generation, Identity: a.Identity,
		PlatformVersion: a.PlatformVersion, CredentialVersion: a.CredentialVersion, CredentialFingerprint: a.CredentialFingerprint}
	if a.CLIProfile != nil {
		profile := *a.CLIProfile
		result.CLIProfile = &profile
	}
	return result, nil
}

func (h notificationRuntimeHost) States(owner RoomIdentity) []notification.State {
	var result []notification.State
	for _, state := range h.manager.notifications.StatesLocked(owner) {
		if state.Metadata().Key.Source == h.source {
			result = append(result, state)
		}
	}
	return result
}
