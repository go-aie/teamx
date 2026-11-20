package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

type inputMCPBinding struct {
	*inputSession
	ctx    context.Context
	active sync.WaitGroup // Admission and deletion serialize under serverAccess.mu.
}

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
	ref, exists, err := readSessionReference(path, id)
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
	input := a.inputBindings[sha256.Sum256([]byte(token))]
	e := a.mcpExecutions[sha256.Sum256([]byte(token))]
	if input != nil {
		input.active.Add(1)
	}
	a.mu.Unlock()
	if input != nil {
		defer input.active.Done()
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(input.ctx, cancel)
		defer stop()
		defer cancel()
		if input.ctx.Err() != nil {
			accessError(w, 403, "identity_mismatch")
			return
		}
		r = r.WithContext(ctx)
		if err := input.check(r.Context()); err != nil {
			accessError(w, 403, "identity_mismatch")
			return
		}
		call := r.Header.Get("X-Teamx-Call")
		if call == "" || len(call) > 128 {
			accessError(w, 400, "missing_call_binding")
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 128<<10+1))
		if err != nil || len(data) > 128<<10 {
			accessError(w, 400, "invalid_request")
			return
		}
		var msg struct {
			Method  string   `json:"method"`
			Digests []string `json:"received_digests"`
		}
		if json.Unmarshal(data, &msg) != nil {
			accessError(w, 400, "invalid_request")
			return
		}
		if msg.Method == "teamx/received" {
			if err := input.receiveGroupBodies(r.Context(), call, msg.Digests); err != nil {
				writeAPIError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(data)))
		ctx = context.WithValue(r.Context(), inputSessionKey{}, input.inputSession)
		ctx = context.WithValue(ctx, inputCallKey{}, call)
		h := a.inputMCPFor(input.service.inboxMode == "cli")
		h.ServeHTTP(w, r.WithContext(ctx))
		return
	}
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
	if e.inbox != nil && e.inbox.service.unifiedInputs {
		h := a.inputMCPFor(e.inbox.service.inboxMode == "cli")
		h.ServeHTTP(w, r.WithContext(ctx))
		return
	}
	a.mcp.ServeHTTP(w, r.WithContext(ctx))
}

func (a *serverAccess) issueInputBinding(s *inputSession) (string, func(), error) {
	if err := s.check(context.Background()); err != nil {
		return "", nil, err
	}
	token, err := randomAccessToken()
	if err != nil {
		return "", nil, err
	}
	key := sha256.Sum256([]byte(token))
	a.mu.Lock()
	if a.inputBindings == nil {
		a.inputBindings = map[[32]byte]*inputMCPBinding{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	binding := &inputMCPBinding{inputSession: s, ctx: ctx}
	a.inputBindings[key] = binding
	a.mu.Unlock()
	return token, sync.OnceFunc(func() {
		a.mu.Lock()
		delete(a.inputBindings, key)
		cancel()
		a.mu.Unlock()
		binding.active.Wait()
		s.storage.discardHistoryResults(s.execution.Turn)
	}), nil
}

// Shared ACP declaration; temporary credentials never become model arguments.
func mcpHTTPConfiguration(endpoint, token string) dsh.MCPHTTPServer {
	return dsh.MCPHTTPServer{Type: "http", Name: "teamx", URL: strings.TrimSuffix(endpoint, "/") + "/mcp",
		Headers: []dsh.MCPHeader{{Name: "Authorization", Value: "Bearer " + token}}}
}

// Both credential paths use the same production tool set and transport policy.
func (a *serverAccess) inputMCPFor(cliInbox bool) *mcpHTTP {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inputMCP == nil {
		a.inputMCP = make(map[bool]*mcpHTTP)
	}
	if a.inputMCP[cliInbox] == nil {
		a.inputMCP[cliInbox] = newHistoryMCPHTTP(cliInbox)
	}
	return a.inputMCP[cliInbox]
}
