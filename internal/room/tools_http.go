package room

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type toolCatalogView struct {
	tool.Definition
	Actions []platform.ActionSpec `json:"actions,omitempty"`
}

func (m *roomManager) toolCatalogViewLocked() []toolCatalogView {
	views := []toolCatalogView{}
	for _, d := range m.toolDefinitions() {
		v := toolCatalogView{Definition: d}
		if caps, err := m.referenceCapabilities(d.Provider); err == nil {
			for _, cap := range caps.Actions {
				if cap.Spec.RequiresAuthorization {
					v.Actions = append(v.Actions, cap.Spec)
				}
			}
			sort.Slice(v.Actions, func(i, j int) bool { return v.Actions[i].Operation < v.Actions[j].Operation })
		}
		views = append(views, v)
	}
	return views
}

type toolFrame struct {
	Stream string `json:"stream"`
	Data   []byte `json:"data,omitempty"`
	Code   int    `json:"code,omitempty"`
}
type toolStream struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	cancel context.CancelFunc
}
type toolStreamWriter struct {
	stream *toolStream
	name   string
}

func (w toolStreamWriter) Write(data []byte) (int, error) {
	w.stream.mu.Lock()
	defer w.stream.mu.Unlock()
	// Bound each frame while preserving arbitrary bytes (JSON base64).
	size := len(data)
	for len(data) > 0 {
		n := min(len(data), 32<<10)
		if err := json.NewEncoder(w.stream.w).Encode(toolFrame{Stream: w.name, Data: data[:n]}); err != nil {
			w.stream.cancel()
			return 0, err
		}
		if err := http.NewResponseController(w.stream.w).Flush(); err != nil {
			w.stream.cancel()
			return 0, err
		}
		data = data[n:]
	}
	return size, nil
}
func (m *roomManager) serveTools(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/tools/run":
		m.serveToolRun(w, r)
		return
	case "/v1/tools/list", "/v1/tools/skill":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
			writeAPIError(w, ErrIdentity)
			return
		}
		var req toolsRequest
		if !decodeToolRequest(w, r, &req) {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(req.Args) != 0 || r.URL.Path == "/v1/tools/list" && req.Tool != "" {
			writeAPIError(w, ErrRequest)
			return
		}
		if err := m.checkToolMemberLocked(r.Context(), req); err != nil {
			writeAPIError(w, err)
			return
		}
		f, err := m.readToolBindingsLocked(req.RoomIdentity)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		if r.URL.Path == "/v1/tools/skill" {
			if err := m.checkToolEntryLocked(req); err != nil {
				writeAPIError(w, err)
				return
			}
			_, d, err := m.resolveToolBindingLocked(f, req)
			if err == nil {
				data, err := entrySkill(d, req.Tool)
				if err == nil {
					w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
					_, _ = w.Write(data)
					return
				}
			}
			writeAPIError(w, ErrRequest)
			return
		}
		selections, err := m.selectionsLocked(f, req.Caller)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		available := []toolSelection{}
		for _, s := range selections {
			if len(s.Capabilities) != 0 {
				available = append(available, s)
			}
		}
		_ = json.NewEncoder(w).Encode(available)
		return
	case "/v1/tools/bindings":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		q := r.URL.Query()
		if q.Get("service_epoch") != m.epoch || !validText(q.Get("caller_id")) {
			writeAPIError(w, ErrIdentity)
			return
		}
		f, err := m.readToolBindingsLocked(RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		// Historical evidence stays on disk, but must not appear enabled after
		// connection rotation or a newly verified account in the same profile.
		platforms, err := m.platforms.Load()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		for i, b := range f.Bindings {
			visible := []platform.CapabilityEvidence{}
			for _, evidence := range b.CapabilityEvidence {
				for _, c := range platforms.Platforms {
					if c.ID != b.PlatformID || c.Generation != b.PlatformGeneration {
						continue
					}
					account, _, err := registry.ResolveAccessAccount(platforms, c)
					if err == nil && evidence.Current(evidence.Kind, platform.AccessContext{Account: account.Identity, CredentialVersion: account.CredentialVersion}, evidence.Target) {
						visible = append(visible, evidence)
					}
				}
			}
			f.Bindings[i].CapabilityEvidence = visible
		}
		// Settings inspect all members; the caller remains the human operator.
		selections := []toolSelection{}
		seenMembers := map[[2]string]bool{}
		for _, b := range f.Bindings {
			key := [2]string{b.AgentID, b.MembershipID}
			if seenMembers[key] {
				continue
			}
			seenMembers[key] = true
			items, e := m.selectionsLocked(f, Caller{ID: b.AgentID, MembershipID: b.MembershipID})
			if e != nil {
				writeAPIError(w, e)
				return
			}
			selections = append(selections, items...)
		}
		_ = json.NewEncoder(w).Encode(struct {
			toolBindingsFile
			Catalog    []toolCatalogView `json:"catalog"`
			Selections []toolSelection   `json:"selections"`
		}{f, m.toolCatalogViewLocked(), selections})
		return
	case "/v1/tools/bind":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var req toolBindingChange
		if !decodeToolRequest(w, r, &req) {
			return
		}
		if err := m.prepareBindingEvidence(r.Context(), &req); err != nil {
			writeAPIError(w, err)
			return
		}
		m.mu.Lock()
		f, pending, err := m.changeToolBindingLocked(r.Context(), req)
		m.mu.Unlock()
		if err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			if notification.Wait(ctx, pending) != nil {
				err = ErrBusy
			}
		}
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(f)
		return
	default:
		http.NotFound(w, r)
	}
}

func decodeToolRequest(w http.ResponseWriter, r *http.Request, out any) bool {
	data, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		writeAPIError(w, invalidField("", "request body exceeds limit or could not be read"))
		return false
	}
	if err := decodeRequestJSON(string(data), out); err != nil {
		writeAPIError(w, err)
		return false
	}
	return true
}
func (m *roomManager) serveToolRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	// One bounded JSON line contains only identity, executable name and argv;
	// everything after it is raw stdin. No credential travels over this protocol.
	reader := bufio.NewReaderSize(r.Body, 64<<10)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	var req toolsRequest
	if err = strictToolJSON(line, &req); err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.EnableFullDuplex()
	w.Header().Set("Connection", "close")
	entryCtx, err := m.withToolEntry(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if entry, ok := entryCtx.Value(toolEntryContextKey{}).(toolEntryContext); ok {
		if dispatch := builtinToolDispatchers[entry.provider]; dispatch != nil {
			dispatch(m, w, r.WithContext(entryCtx), req)
			return
		}
	}
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	m.mu.Lock()
	process, call, ctx, err := m.prepareToolLocked(r.Context(), req)
	m.mu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer m.finishPlatformCall(call)
	if call.account.Authorization.CredentialID != "" {
		useCtx, credential, finish, err := m.acquireGitLabAccess(ctx, req.RoomIdentity, call.account, call.secret, maxGitLabTokenUse)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		defer func() { _ = finish() }()
		ctx = useCtx
		process.Values["token"] = credential.Token.Reveal()
		// The registered process receives only access material, never refresh secrets.
		process.Definition.Environment = cloneToolEnv(process.Definition.Environment)
	}
	stopIO := watchToolIO(ctx, controller)
	defer stopIO()

	// A file descriptor avoids exec's input-copy goroutine waiting forever when
	// the executable exits without consuming interactive stdin.
	input, feed, err := os.Pipe()
	if err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	defer input.Close()
	defer feed.Close()
	go func() { _, _ = io.Copy(feed, reader); _ = feed.Close() }()
	stream := &toolStream{w: w, cancel: call.cancel}
	process.Input = input
	process.Output = toolStreamWriter{stream, "stdout"}
	process.Error = toolStreamWriter{stream, "stderr"}
	w.Header().Set("Content-Type", "application/x-ndjson")
	// A command may finish before request stdin reaches EOF. Close this duplex
	// connection so a read deadline or unread upload cannot affect a later call.
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	_ = controller.Flush()
	code := process.Run(ctx, req.Args)
	_ = input.Close()
	// Stop the request upload even when stdin was never read by the tool.
	_ = controller.SetReadDeadline(time.Now())
	_ = r.Body.Close()
	stream.mu.Lock()
	_ = json.NewEncoder(w).Encode(toolFrame{Stream: "exit", Code: code})
	_ = controller.Flush()
	stream.mu.Unlock()
}

// A disconnected or slow reader must not prevent binding revocation and
// shutdown, whether output came from a process or a bounded built-in handler.
func watchToolIO(ctx context.Context, controller *http.ResponseController) func() {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = controller.SetReadDeadline(time.Now())
			_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
		case <-done:
		}
	}()
	return func() { close(done); <-stopped }
}
