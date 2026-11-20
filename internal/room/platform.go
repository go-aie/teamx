package room

import (
	"context"
	"path/filepath"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
)

// Static composition points are independent: a provider can offer platforms,
// notification sources or tools without implementing the other capabilities.
type connectionProvider struct {
	LegacyIdentity func(string, platform.Validation) (platform.AccountIdentity, bool)
	Normalize      func(name, origin string) (string, string, error)
	ValidProfile   func(string, platform.CLIProfile) bool
	VerifyProfile  func(context.Context, string, platform.CLIProfile) platform.Validation
}

func (m *roomManager) initPlatform() {
	if m.platformCatalog == nil {
		m.platformCatalog = defaultPlatformCatalog()
	}
	if m.connectionProviders == nil {
		m.connectionProviders = make(map[string]connectionProvider)
		for _, definition := range m.platformCatalog.Definitions() {
			if definition.Connection == nil {
				continue
			}
			c := definition.Connection
			adapter := connectionProvider{Normalize: c.Normalize, LegacyIdentity: c.LegacyIdentity, ValidProfile: c.ValidProfile}
			// Resource-bound verification remains at the outer composition point.
			switch definition.Type.Provider {
			case "scion":
				adapter.VerifyProfile = m.verifyScionProfile
			case "lark":
				adapter.VerifyProfile = func(ctx context.Context, origin string, p platform.CLIProfile) platform.Validation {
					return m.larkClients.VerifyIdentity(ctx, origin, p)
				}
			}
			m.connectionProviders[definition.Type.Provider] = adapter
		}
	}
	m.platforms = registry.New(registry.Store{
		Path: filepath.Join(m.store.dir, "platform-connections.json"),
		Check: func(writing bool) error {
			if writing && m.closing {
				return ErrClosing
			}
			return m.store.check(writing)
		},
		Save: m.store.save,
		Changed: func() {
			m.updates.mark(uiUpdate{Topic: "platforms"})
			m.updates.markSubscribedRooms("tools", "notifications", "interactions", "workspace")
		},
	}, func(name, provider, origin string) (string, string, error) {
		adapter, ok := m.connectionProviders[provider]
		if !ok || adapter.Normalize == nil {
			return "", "", registry.ErrRequest
		}
		return adapter.Normalize(name, origin)
	}, m.platformCatalog.Types())
	m.platforms.SetIdentityResolver(func(provider, origin string, v platform.Validation) (platform.AccountIdentity, bool) {
		a := m.connectionProviders[provider]
		if a.LegacyIdentity == nil {
			return v.Account, v.Account.Valid()
		}
		return a.LegacyIdentity(origin, v)
	})
	m.platforms.SetProfileSupport(func(provider string) bool {
		a := m.connectionProviders[provider]
		return a.VerifyProfile != nil
	}, func(provider, origin string, p platform.CLIProfile) bool {
		a, ok := m.connectionProviders[provider]
		return ok && a.ValidProfile != nil && a.ValidProfile(origin, p)
	}, func(ctx context.Context, provider, origin string, p platform.CLIProfile) platform.Validation {
		a := m.connectionProviders[provider]
		if a.VerifyProfile == nil {
			return platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "invalid_response"}
		}
		return a.VerifyProfile(ctx, origin, p)
	})
	m.initPlatformNotifications()
	m.initPlatformReferences()
}
