package room

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

const oauthRefreshWindow = 30 * time.Second
const maxGitLabTokenUse = 5 * time.Minute

type oauthRefreshFlight struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error // published before closing done
}
type gitlabTokenLease struct {
	credential platform.AccessCredential
	account    connection.Account
	ctx        context.Context
	call       *platformCall
}

// Requires manager.mu. This is authorization evidence only, with no synchronous
// network work or raw token resolution under the service gate.
func (m *roomManager) oauthAccountLocked(expected connection.Account) (connection.Account, connection.OAuthCredential, error) {
	if m.closing {
		return connection.Account{}, connection.OAuthCredential{}, ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return connection.Account{}, connection.OAuthCredential{}, err
	}
	file, err := m.connections.Load()
	if err != nil {
		return connection.Account{}, connection.OAuthCredential{}, err
	}
	for _, c := range file.Connections {
		if c.ID != expected.ConnectionID {
			continue
		}
		a, err := connection.ResolveAuthorization(file, c)
		if err != nil {
			return a, connection.OAuthCredential{}, err
		}
		if a != expected || a.Authorization.CredentialID == "" {
			return a, connection.OAuthCredential{}, ErrConflict
		}
		cred, _, err := connection.OAuthRecord(file, c.CredentialID)
		return a, cred, err
	}
	return connection.Account{}, connection.OAuthCredential{}, ErrConflict
}

// acquireGitLabToken is a waiting service boundary, never called with manager.mu
// held. The caller has already authorized the Room/member/business operation.
// Each business request retains the lease through complete response settlement.
func (m *roomManager) acquireGitLabToken(ctx context.Context, owner RoomIdentity, expected connection.Account, use time.Duration) (*gitlabTokenLease, error) {
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
			call := &platformCall{owner: owner, connectionID: account.ConnectionID, tool: "gitlab-token", cancel: cancel, done: make(chan struct{})}
			if m.platformCalls == nil {
				m.platformCalls = map[*platformCall]bool{}
			}
			m.platformCalls[call] = true
			lease := &gitlabTokenLease{credential: platform.AccessCredential{Kind: "oauth", Token: platform.NewSecret(cred.AccessToken)}, account: account, ctx: leaseCtx, call: call}
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
			pending = append(pending, lease.call.done)
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

func (m *roomManager) refreshGitLabToken(ctx context.Context, expected connection.Account, pending []<-chan struct{}) error {
	// A canceled context is not settlement. The lease owner releases only after
	// its HTTP body/process has actually finished, including cleanup.
	if err := notification.Wait(ctx, pending); err != nil {
		return err
	}
	m.mu.Lock()
	_, _, err := m.oauthAccountLocked(expected)
	if err == nil {
		err = ctx.Err()
	}
	var ticket connection.OAuthRefresh
	if err == nil {
		ticket, err = m.connections.BeginOAuthRefresh(expected.Authorization)
	}
	transport := m.platformHTTP
	m.mu.Unlock()
	if err != nil {
		return err
	}
	input := gitlab.OAuthRefreshRequest{Origin: ticket.Application.Origin, ClientID: ticket.Application.ClientID, ClientSecret: platform.NewSecret(ticket.Application.ClientSecret), RedirectURI: ticket.Application.RedirectURI, RefreshToken: ticket.RefreshToken, UserID: ticket.Evidence.UserID, Scopes: ticket.Evidence.Scopes}
	tokens, queryErr := gitlab.RefreshOAuth(ctx, input, transport)
	outcome := "active"
	if queryErr != nil {
		outcome = gitlab.OAuthRefreshState(queryErr)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Shutdown may forbid a final save. Durable refresh_pending already blocks
	// future admission and survives process termination without a POST replay.
	if err := m.connections.CompleteOAuthRefresh(ticket, tokens, outcome); err != nil {
		// Logout may have durably blocked the grant while the destructive POST was in flight. Keep its returned candidate solely for cleanup.
		if tokens.AccessToken.Reveal() != "" {
			_, _ = m.connections.KeepOAuthCandidate(ticket.Application, tokens)
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
func (m *roomManager) acquireGitLabAccess(ctx context.Context, owner RoomIdentity, account connection.Account, secret platform.Secret, use time.Duration) (context.Context, platform.AccessCredential, func() error, error) {
	if account.Authorization.CredentialID == "" || !strings.Contains(" "+account.Authorization.Scopes+" ", " api ") {
		return nil, platform.AccessCredential{}, nil, platform.ErrCredential
	}
	lease, err := m.acquireGitLabToken(ctx, owner, account, use)
	if err != nil {
		return nil, platform.AccessCredential{}, nil, err
	}
	return lease.ctx, lease.credential, func() error { return m.finishGitLabToken(lease) }, nil
}

func (m *roomManager) gitlabAccountRead(owner RoomIdentity, account connection.Account, secret platform.Secret) gitlab.ReadAPI {
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

func (m *roomManager) verifyGitlabAccountProject(ctx context.Context, owner RoomIdentity, a connection.Account, secret platform.Secret, project string, override func(context.Context, string, string, workspaceSecret) (int64, error)) (id int64, err error) {
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
