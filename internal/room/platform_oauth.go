package room

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
	"github.com/go-aie/teamx/internal/platform/registry"
)

const oauthRefreshWindow = 30 * time.Second
const maxGitLabTokenUse = 5 * time.Minute

type oauthRefreshFlight struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error // published before closing done
}
type gitlabTokenLease struct {
	applicationUse *gitlaboauth.Use
	credential     platform.AccessCredential
	account        registry.Account
	ctx            context.Context
	call           *platformCall
}

// Requires manager.mu. This is authorization evidence only, with no synchronous
// network work or raw token resolution under the service gate.
func (m *roomManager) oauthAccountLocked(expected registry.Account) (registry.Account, registry.OAuthCredential, error) {
	if err := m.withGitLabGrantOwnerLocked("", func() error { return nil }); err != nil {
		return registry.Account{}, registry.OAuthCredential{}, err
	}
	if m.closing {
		return registry.Account{}, registry.OAuthCredential{}, ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return registry.Account{}, registry.OAuthCredential{}, err
	}
	file, err := m.platforms.Load()
	if err != nil {
		return registry.Account{}, registry.OAuthCredential{}, err
	}
	for _, c := range file.Platforms {
		if c.ID != expected.PlatformID {
			continue
		}
		a, err := registry.ResolveAuthorization(file, c)
		if err != nil {
			return a, registry.OAuthCredential{}, err
		}
		if a != expected || a.Authorization.CredentialID == "" {
			return a, registry.OAuthCredential{}, ErrConflict
		}
		cred, app, err := registry.OAuthRecord(file, c.CredentialID)
		if err == nil {
			_, err = m.currentGitLabApplicationLocked(app)
		}
		return a, cred, err
	}
	return registry.Account{}, registry.OAuthCredential{}, ErrConflict
}

// acquireGitLabToken is a waiting service boundary, never called with manager.mu
// held. The caller has already authorized the Room/member/business operation.
// Each business request retains the lease through complete response settlement.
func (m *roomManager) acquireGitLabToken(ctx context.Context, owner RoomIdentity, expected registry.Account, use time.Duration) (*gitlabTokenLease, error) {
	if use <= 0 || use > maxGitLabTokenUse {
		return nil, ErrRequest
	}
	refreshed := false
	for {
		m.mu.Lock()
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return nil, err
		}
		account, cred, err := m.oauthAccountLocked(expected)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		id := account.Authorization.CredentialID
		if f := m.oauthFlights[id]; f != nil {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-f.done:
			}
			if f.err != nil {
				return nil, f.err
			}
			refreshed = true
			continue
		}
		// A persisted pending record without its live flight is crash evidence.
		if cred.State != "active" {
			m.mu.Unlock()
			return nil, platform.ErrCredential
		}
		if cred.ExpiresAt.After(time.Now().Add(use + oauthRefreshWindow)) {
			leaseCtx, cancel := context.WithTimeout(ctx, use)
			file, e := m.platforms.Load()
			if e != nil {
				cancel()
				m.mu.Unlock()
				return nil, e
			}
			_, app, e := registry.OAuthRecord(file, id)
			if e == nil {
				app, e = m.currentGitLabApplicationLocked(app)
			}
			operation, eID := newOpaqueID()
			if e == nil {
				e = eID
			}
			var appUse *gitlaboauth.Use
			if e == nil {
				appUse, e = m.gitlabApplications().Begin(leaseCtx, app, operation, "")
			}
			if e != nil {
				cancel()
				m.mu.Unlock()
				return nil, e
			}
			leaseCtx = appUse.Context()
			call := &platformCall{owner: owner, platformID: account.PlatformID, tool: "gitlab-token"}
			call.Call = m.platformCalls.Register(call, cancel)
			lease := &gitlabTokenLease{applicationUse: appUse, credential: platform.AccessCredential{Kind: "oauth", Token: platform.NewSecret(cred.AccessToken)}, account: account, ctx: leaseCtx, call: call}
			if m.oauthLeases == nil {
				m.oauthLeases = map[string]map[*gitlabTokenLease]bool{}
			}
			if m.oauthLeases[id] == nil {
				m.oauthLeases[id] = map[*gitlabTokenLease]bool{}
			}
			m.oauthLeases[id][lease] = true
			m.mu.Unlock()
			return lease, nil
		}
		// Do not loop on a server issuing a lifetime shorter than this bounded use.
		if refreshed {
			m.mu.Unlock()
			return nil, platform.ErrCredential
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		f := &oauthRefreshFlight{done: make(chan struct{}), cancel: cancel}
		if m.oauthFlights == nil {
			m.oauthFlights = map[string]*oauthRefreshFlight{}
		}
		m.oauthFlights[id] = f // close admission before waiting for old token users
		var pending []<-chan struct{}
		for lease := range m.oauthLeases[id] {
			pending = append(pending, lease.call.Done())
		}
		m.mu.Unlock()

		err = m.refreshGitLabToken(refreshCtx, expected, pending)
		cancel()
		m.mu.Lock()
		f.err = err
		delete(m.oauthFlights, id)
		close(f.done)
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		refreshed = true
	}
}

func (m *roomManager) refreshGitLabToken(ctx context.Context, expected registry.Account, pending []<-chan struct{}) error {
	// A canceled context is not settlement. The lease owner releases only after
	// its HTTP body/process has actually finished, including cleanup.
	if err := platforms.WaitCalls(ctx, pending); err != nil {
		return err
	}
	m.mu.Lock()
	_, _, err := m.oauthAccountLocked(expected)
	if err == nil {
		err = ctx.Err()
	}
	var appUse *gitlaboauth.Use
	if err == nil {
		file, e := m.platforms.Load()
		err = e
		if err == nil {
			_, app, e := registry.OAuthRecord(file, expected.Authorization.CredentialID)
			err = e
			if err == nil {
				app, err = m.currentGitLabApplicationLocked(app)
			}
			if err == nil {
				operation, e := newOpaqueID()
				err = e
				if err == nil {
					appUse, err = m.gitlabApplications().Begin(ctx, app, operation, "")
				}
			}
		}
	}
	var ticket registry.OAuthRefresh
	if err == nil {
		ticket, err = m.platforms.BeginOAuthRefresh(expected.Authorization)
	}
	transport := m.platformHTTP
	m.mu.Unlock()
	if appUse != nil {
		defer appUse.Close()
	}
	if err != nil {
		return err
	}
	tokens, queryErr := appUse.Refresh(ticket.RefreshToken, ticket.Evidence.UserID, ticket.Evidence.Scopes, transport)
	outcome := "active"
	if queryErr != nil {
		outcome = gitlab.OAuthRefreshState(queryErr)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Shutdown may forbid a final save. Durable refresh_pending already blocks
	// future admission and survives process termination without a POST replay.
	// A stale application must not reactivate a grant. Preserve the destructive
	// refresh result as unusable evidence, without replaying it after restart.
	if queryErr == nil {
		if e := appUse.Commit(func() error { return nil }); e != nil {
			queryErr = e
			outcome = "refresh_unknown"
		}
	}
	commit := func() error { return m.platforms.CompleteOAuthRefresh(ticket, tokens, outcome) }
	var commitErr error
	if outcome == "active" {
		commitErr = appUse.Commit(func() error { return m.withGitLabGrantOwnerLocked("", commit) })
	} else {
		commitErr = commit()
	}
	if err := commitErr; err != nil {
		// Logout may have durably blocked the grant while the destructive POST was in flight. Keep its returned candidate solely for cleanup.
		if tokens.AccessToken.Reveal() != "" {
			_, _ = m.platforms.KeepOAuthCandidate(ticket.Application, tokens)
		}
		return err
	}
	return queryErr
}

// finishGitLabToken must run after the bounded operation fully settles. A
// normal refresh does not invalidate a completed result; grant changes do.
func (m *roomManager) finishGitLabToken(lease *gitlabTokenLease) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, _, err := m.oauthAccountLocked(lease.account)
	if err == nil {
		err = lease.ctx.Err()
	}
	if lease.applicationUse != nil {
		if e := lease.applicationUse.Commit(func() error { return nil }); err == nil {
			err = e
		}
		lease.applicationUse.Close()
	}
	delete(m.oauthLeases[lease.account.Authorization.CredentialID], lease)
	if len(m.oauthLeases[lease.account.Authorization.CredentialID]) == 0 {
		delete(m.oauthLeases, lease.account.Authorization.CredentialID)
	}
	m.finishPlatformCallLocked(lease.call)
	lease.credential = platform.AccessCredential{}
	return err
}

// acquireGitLabAccess resolves material only for a single bounded operation.
// Authorization is checked independently of token rotation. No business request
// is retried at this boundary.
func (m *roomManager) acquireGitLabAccess(ctx context.Context, owner RoomIdentity, account registry.Account, secret platform.Secret, use time.Duration) (context.Context, platform.AccessCredential, func() error, error) {
	if account.Authorization.CredentialID == "" || !strings.Contains(" "+account.Authorization.Scopes+" ", " api ") {
		return nil, platform.AccessCredential{}, nil, platform.ErrCredential
	}
	lease, err := m.acquireGitLabToken(ctx, owner, account, use)
	if err != nil {
		return nil, platform.AccessCredential{}, nil, err
	}
	return lease.ctx, lease.credential, func() error { return m.finishGitLabToken(lease) }, nil
}

func (m *roomManager) gitlabAccountRead(owner RoomIdentity, account registry.Account, secret platform.Secret) gitlab.ReadAPI {
	return func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (response gitlab.APIResponse, err error) {
		useCtx, credential, finish, err := m.acquireGitLabAccess(ctx, owner, account, secret, 30*time.Second)
		if err != nil {
			return response, err
		}
		defer func() {
			if checkErr := finish(); checkErr != nil {
				response = gitlab.APIResponse{}
				err = checkErr
			}
		}()
		return gitlab.NewReadAPIWithCredential(account.URL, credential, m.platformHTTP)(useCtx, path, q, h, limit)
	}
}

func (m *roomManager) verifyGitlabAccountProject(ctx context.Context, owner RoomIdentity, a registry.Account, secret platform.Secret, project string, override func(context.Context, string, string, workspaceSecret) (int64, error)) (id int64, err error) {
	useCtx, credential, finish, err := m.acquireGitLabAccess(ctx, owner, a, secret, 30*time.Second)
	if err != nil {
		return 0, err
	}
	defer func() {
		if checkErr := finish(); checkErr != nil {
			id = 0
			err = checkErr
		}
	}()
	if override != nil {
		return override(useCtx, a.URL, project, workspaceSecret{value: credential.Token.Reveal()})
	}
	return gitlab.VerifyProjectWithCredential(useCtx, a.URL, project, credential, m.platformHTTP)
}
