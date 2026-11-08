package room

import (
	"context"
	"path/filepath"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/scion"
)

// Static composition points are independent: a provider can offer connections,
// notification sources or tools without implementing the other capabilities.
type connectionProvider struct {
	LegacyIdentity func(string, platform.Validation) (platform.AccountIdentity, bool)
	Normalize      func(name, origin string) (string, string, error)
	ValidProfile   func(string, platform.CLIProfile) bool
	VerifyProfile  func(context.Context, string, platform.CLIProfile) platform.Validation
}

func (m *roomManager) initPlatform() {
	if m.connectionProviders == nil {
		m.connectionProviders = map[string]connectionProvider{
			"gitlab": {Normalize: connection.Normalize, LegacyIdentity: gitlab.LegacyAccountIdentity},
			"scion": {Normalize: connection.Normalize, ValidProfile: func(_ string, p platform.CLIProfile) bool {
				return scion.ValidProfile(p)
			}, VerifyProfile: func(ctx context.Context, origin string, p platform.CLIProfile) platform.Validation {
				return scion.VerifyProfile(ctx, m.store.dir, origin, p)
			}},
			"lark": {Normalize: connection.Normalize, ValidProfile: func(origin string, p platform.CLIProfile) bool {
				return lark.ValidProfile(p) && origin == lark.Origin(p.Brand)
			}, VerifyProfile: func(ctx context.Context, origin string, p platform.CLIProfile) platform.Validation {
				return m.larkClients.VerifyIdentity(ctx, origin, p)
			}},
		}
	}
	m.connections = connection.New(connection.Store{
		Path: filepath.Join(m.store.dir, "platform-connections.json"),
		Check: func(writing bool) error {
			if writing && m.closing {
				return ErrClosing
			}
			return m.store.check(writing)
		},
		Save: m.store.save,
	}, func(name, provider, origin string) (string, string, error) {
		adapter, ok := m.connectionProviders[provider]
		if !ok || adapter.Normalize == nil {
			return "", "", connection.ErrRequest
		}
		return adapter.Normalize(name, origin)
	})
	m.connections.SetIdentityResolver(func(provider, origin string, v platform.Validation) (platform.AccountIdentity, bool) {
		a := m.connectionProviders[provider]
		if a.LegacyIdentity == nil {
			return v.Account, v.Account.Valid()
		}
		return a.LegacyIdentity(origin, v)
	})
	m.connections.SetProfileSupport(func(provider string) bool {
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
