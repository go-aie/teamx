package room

import (
	"context"
	"path/filepath"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// Static composition points are independent: a provider can offer connections,
// notification sources or tools without implementing the other capabilities.
type connectionProvider struct {
	Normalize func(name, origin string) (string, string, error)
	Verify    func(context.Context, string, platform.Secret) platform.Validation
}

func (m *roomManager) initPlatform() {
	if m.connectionProviders == nil {
		m.connectionProviders = map[string]connectionProvider{
			"gitlab": {Normalize: connection.Normalize, Verify: gitlab.VerifyIdentity},
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
		if !ok || adapter.Normalize == nil || adapter.Verify == nil {
			return "", "", connection.ErrRequest
		}
		return adapter.Normalize(name, origin)
	}, func(ctx context.Context, provider, origin string, secret platform.Secret) platform.Validation {
		adapter, ok := m.connectionProviders[provider]
		if !ok || adapter.Verify == nil {
			return platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "invalid_response"}
		}
		return adapter.Verify(ctx, origin, secret)
	})
	m.initPlatformNotifications()
}

func verifyGitLabProject(ctx context.Context, origin, project string, secret workspaceSecret) (int64, error) {
	return gitlab.VerifyProject(ctx, origin, project, platform.NewSecret(secret.value))
}
