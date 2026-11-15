package room

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

type mcpIdentityKey struct{}
type mcpIdentity struct {
	executionBinding
	Generation string
}

type mcpExecution struct {
	identity mcpIdentity
	store    *Store
	ctx      context.Context
	inbox    *mcpInboxSession
}

func (e *mcpExecution) validate() error {
	s := e.store
	if e.inbox != nil && (e.inbox.service.store != s || e.inbox.service.info != e.identity.ServiceInfo || e.inbox.service.closing.Load() || e.inbox.service.restarting.Load()) {
		return ErrIdentity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := MemberIdentity{RoomIdentity: e.identity.RoomIdentity, AgentID: e.identity.Caller.ID, MembershipID: e.identity.Caller.MembershipID}
	path, _, err := s.memberPath(id, "session.json", false)
	if err != nil {
		return err
	}
	ref, exists, err := readDSHReference(path, id)
	if err != nil {
		return err
	}
	if !exists || ref.Pending || ref.Directory != e.identity.Generation || e.ctx.Err() != nil {
		return ErrIdentity
	}
	return nil
}

// A separate credential namespace prevents MCP tokens from authorizing CLI or
// operator routes, and prevents ordinary execution tokens from authorizing MCP.
func (a *serverAccess) issueMCPExecution(binding executionBinding, store *Store, generation string) (string, func(), error) {
	return a.issueMCP(binding, store, generation, nil)
}

func (a *serverAccess) issueMCP(binding executionBinding, store *Store, generation string, inbox *mcpInboxSession) (string, func(), error) {
	if store == nil || generation == "" || binding.ServiceEpoch == "" || binding.Caller.Kind != "agent" {
		return "", nil, ErrIdentity
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &mcpExecution{identity: mcpIdentity{binding, generation}, store: store, ctx: ctx, inbox: inbox}
	if err := e.validate(); err != nil {
		cancel()
		return "", nil, err
	}
	token, err := randomAccessToken()
	if err != nil {
		cancel()
		return "", nil, err
	}
	hash := sha256.Sum256([]byte(token))
	a.mu.Lock()
	if a.mcpExecutions == nil {
		a.mcpExecutions = make(map[[32]byte]*mcpExecution)
	}
	a.mcpExecutions[hash] = e
	a.mu.Unlock()
	return token, func() {
		a.mu.Lock()
		delete(a.mcpExecutions, hash)
		cancel()
		a.mu.Unlock()
		if inbox != nil {
			inbox.close()
		}
	}, nil
}

func (a *serverAccess) serveMCP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !validAccessToken(token) {
		accessError(w, 401, "unauthorized")
		return
	}
	a.mu.Lock()
	e := a.mcpExecutions[sha256.Sum256([]byte(token))]
	a.mu.Unlock()
	if e == nil {
		accessError(w, 401, "unauthorized")
		return
	}
	if err := e.validate(); err != nil {
		accessError(w, 403, "identity_mismatch")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	defer cancel()
	if e.ctx.Err() != nil {
		accessError(w, 403, "identity_mismatch")
		return
	}
	ctx = context.WithValue(ctx, mcpIdentityKey{}, e.identity)
	ctx = context.WithValue(ctx, mcpExecutionKey{}, e)
	a.mcp.ServeHTTP(w, r.WithContext(ctx))
}

// Shared ACP declaration; temporary credentials never become model arguments.
func mcpHTTPConfiguration(endpoint, token string) dsh.MCPHTTPServer {
	return dsh.MCPHTTPServer{Type: "http", Name: "teamx", URL: strings.TrimSuffix(endpoint, "/") + "/mcp",
		Headers: []dsh.MCPHeader{{Name: "Authorization", Value: "Bearer " + token}}}
}
