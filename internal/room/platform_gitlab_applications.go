package room

import (
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func (m *roomManager) gitlabApplications() gitlaboauth.Source {
	if m.config.gitlabApplications != nil {
		return m.config.gitlabApplications
	}
	return gitlaboauth.Personal{Load: m.platforms.Load}
}

// Discovery overlays public application metadata in memory only. Grant reads
// and writes continue to use the original Space snapshot and its writer.
func (m *roomManager) gitlabDiscoveryLocked(file registry.Snapshot) (registry.Snapshot, error) {
	apps, err := m.gitlabApplications().Applications()
	if err != nil {
		return file, err
	}
	file.OAuthApplications = apps
	return file, nil
}
func (m *roomManager) currentGitLabApplicationLocked(saved registry.OAuthApplication) (registry.OAuthApplication, error) {
	if saved.Source == "" {
		return saved, m.gitlabApplications().Check(saved)
	}
	apps, err := m.gitlabApplications().Applications()
	if err != nil {
		return registry.OAuthApplication{}, err
	}
	for _, a := range apps {
		old := saved
		old.Version = a.Version
		if saved.Version <= a.Version && gitlaboauth.Equal(old, a) {
			return a, nil
		}
	}
	return registry.OAuthApplication{}, platform.ErrCredential
}

func (m *roomManager) gitlabEnabled() bool {
	_, disabled := m.gitlabApplications().(gitlaboauth.Disabled)
	return !disabled
}

// The user lifecycle and this final grant write share the user directory gate.
// Disabling a user cannot interleave between its final check and activation of
// a grant. No network call or application lookup runs inside this callback.
func (m *roomManager) withGitLabGrantOwnerLocked(owner string, commit func() error) error {
	a := m.config.access
	if a == nil {
		return commit()
	}
	subject := a.subject()
	if owner != "" && owner != subject.UserID {
		return ErrIdentity
	}
	if a.revoked.Load() {
		return ErrIdentity
	}
	users := a.authority().users
	if users == nil {
		if _, err := a.authorizeSpace(subject, a.space.SpaceID); err != nil {
			return err
		}
		return commit()
	}
	users.mu.Lock()
	defer users.mu.Unlock()
	if users.failed || a.revoked.Load() || a.identity == nil || a.identity.Space.Status != "active" || a.identity.Space.ID != a.space.SpaceID || a.space.OwnerUserID != subject.UserID || a.identity.Space.OwnerUserID != subject.UserID {
		return ErrIdentity
	}
	for _, entry := range users.saved.Users {
		if entry.ID == subject.UserID && entry.Status == "active" && entry.AuthVersion == subject.AuthVersion {
			return commit()
		}
	}
	return ErrIdentity
}
