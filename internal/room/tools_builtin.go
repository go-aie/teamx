package room

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
)

// Composition registers bounded dispatchers beside the existing reference
// adapters. They share platform entries, bindings and call admission.
var builtinToolDispatchers = map[string]func(*roomManager, http.ResponseWriter, *http.Request, toolsRequest){
	"lark": (*roomManager).serveBuiltinTool,
}

func builtinToolHelp(entry string) string {
	return fmt.Sprintf("Usage:\n  teamx tools run %s read --object REF --target REF [--scope content|context] [--cursor CURSOR]\n  teamx tools run %s actions resolve --request JSON\n  teamx tools run %s actions execute --request JSON\nRead uses saved native message references, bounded context and the selected platform account. Actions use the existing managed action protocol, grants and receipts; use teamx actions resolve --help and teamx actions execute --help for request schemas. Arbitrary CLI commands and authentication are unavailable.\n", entry, entry, entry)
}

// Only registered built-in operations enter here. In particular, this is never
// a pass-through to lark-cli and cannot switch a profile or its global login.
func (m *roomManager) serveBuiltinTool(w http.ResponseWriter, r *http.Request, req toolsRequest) {
	if len(req.Args) > 4096 {
		writeAPIError(w, ErrRequest)
		return
	}
	help := len(req.Args) == 0 || len(req.Args) == 1 && (req.Args[0] == "help" || req.Args[0] == "--help" || req.Args[0] == "-h")
	if len(req.Args) == 2 && req.Args[0] == "read" && (req.Args[1] == "--help" || req.Args[1] == "-h") {
		help = true
	}
	m.mu.Lock()
	_, call, ctx, err := m.preparePlatformCallLocked(r.Context(), req, help)
	m.mu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer m.finishPlatformCall(call)
	stopIO := watchToolIO(ctx, http.NewResponseController(w))
	defer stopIO()
	var data []byte
	if help {
		data = []byte(builtinToolHelp(req.Tool))
	} else {
		var result any
		result, err = m.runBuiltinTool(ctx, req)
		if err != nil {
			writeLarkReadError(w, err)
			return
		}
		data, err = json.Marshal(result)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		data = append(data, '\n')
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Connection", "close")
	for len(data) != 0 {
		n := min(len(data), 32<<10)
		if err := json.NewEncoder(w).Encode(toolFrame{Stream: "stdout", Data: data[:n]}); err != nil {
			return
		}
		data = data[n:]
	}
	_ = json.NewEncoder(w).Encode(toolFrame{Stream: "exit", Code: 0})
}

func (m *roomManager) runBuiltinTool(ctx context.Context, req toolsRequest) (any, error) {
	if len(req.Args) == 0 {
		return nil, ErrRequest
	}
	switch req.Args[0] {
	case "read":
		var q larkTargetRequest
		f := flag.NewFlagSet("read", flag.ContinueOnError)
		f.SetOutput(io.Discard)
		f.StringVar(&q.ObjectRef, "object", "", "object_ref")
		f.StringVar(&q.TargetRef, "target", "", "target_ref")
		f.StringVar(&q.Scope, "scope", "content", "content or context")
		f.StringVar(&q.Cursor, "cursor", "", "continuation")
		if f.Parse(req.Args[1:]) != nil || f.NArg() != 0 || !validOpaqueRef(q.ObjectRef, "ref_") || !validOpaqueRef(q.TargetRef, "ref_") || q.Scope != "content" && q.Scope != "context" {
			return nil, ErrRequest
		}
		return m.readLark(ctx, larkReadRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, Target: &q})
	case "actions":
		if len(req.Args) != 4 || req.Args[2] != "--request" {
			return nil, ErrRequest
		}
		api := apiRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}
		switch req.Args[1] {
		case "resolve":
			var q actionResolveRequest
			if err := strictToolJSON([]byte(req.Args[3]), &q); err != nil {
				return nil, ErrRequest
			}
			return m.resolveActionTarget(ctx, api, q)
		case "execute":
			var q agentActionRequest
			if err := strictToolJSON([]byte(req.Args[3]), &q); err != nil {
				return nil, ErrRequest
			}
			m.mu.Lock()
			svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
			m.mu.Unlock()
			if svc == nil {
				return nil, ErrIdentity
			}
			id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
			action, err := svc.store.agentAction(id, q)
			if err != nil {
				return nil, err
			}
			return m.executeReferenceAction(ctx, api, action)
		}
	}
	return nil, ErrRequest
}
