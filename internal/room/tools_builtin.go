package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-aie/teamx/internal/app/platformtools"
)

// This adapter captures identity once; all mutable state remains with its
// original owner. No HTTP writer/request enters the application runtime.
type builtinToolHost struct {
	manager *roomManager
	request toolsRequest
}

func (h builtinToolHost) Admit(ctx context.Context, help bool) (context.Context, func(), error) {
	m := h.manager
	m.mu.Lock()
	_, call, ctx, err := m.preparePlatformCallLocked(ctx, h.request, help)
	m.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	return ctx, func() { m.finishPlatformCall(call) }, nil
}
func (h builtinToolHost) ActionHelp(entry, op string) string { return builtinActionHelpText(entry, op) }
func (h builtinToolHost) Read(ctx context.Context, q platformtools.ReadRequest) (json.RawMessage, error) {
	result, err := h.manager.readLark(ctx, larkReadRequest{ServiceInfo: h.request.ServiceInfo, Caller: h.request.Caller, Target: &q})
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
func (h builtinToolHost) Resolve(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var q actionResolveRequest
	if err := decodeRequestJSON(string(body), &q); err != nil {
		return nil, err
	}
	result, err := h.manager.resolveActionTarget(ctx, apiRequest{ServiceInfo: h.request.ServiceInfo, Caller: h.request.Caller}, q)
	if err != nil {
		return nil, err
	}
	return json.Marshal(projectActionTarget(result))
}
func (h builtinToolHost) Execute(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var q agentActionRequest
	if err := decodeRequestJSON(string(body), &q); err != nil {
		return nil, err
	}
	m, req := h.manager, h.request
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
	result, err := m.executeReferenceAction(ctx, apiRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}, action)
	if err != nil {
		return nil, err
	}
	return json.Marshal(projectActionReceipt(result, action))
}

func toolRuntimeError(err error) error {
	var d *platformtools.Diagnostic
	if errors.As(err, &d) {
		if d.Field == "" && d.Message == "invalid request" {
			return ErrRequest
		}
		if d.Conflict {
			return conflictReason(d.Field, d.Message, d.Hint)
		}
		return invalidField(d.Field, d.Message)
	}
	return err
}

// Only framing and I/O cancellation live here. The prepared application call
// settles after the last frame, so revocation and close still join slow output.
func (m *roomManager) serveBuiltinTool(w http.ResponseWriter, r *http.Request, req toolsRequest, bind platformtools.Bind) {
	if len(req.Args) > 4096 {
		writeAPIError(w, ErrRequest)
		return
	}
	prepared, err := platformtools.Prepare(r.Context(), req.Tool, req.Args, bind, builtinToolHost{m, req})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer prepared.Close()
	stopIO := watchToolIO(prepared.Context, http.NewResponseController(w))
	defer stopIO()
	data, err := prepared.Run()
	if err != nil {
		err = toolRuntimeError(err)
		if len(req.Args) > 0 && req.Args[0] == "actions" {
			writeAPIError(w, agentActionDiagnostic(err))
		} else {
			writeLarkReadError(w, err)
		}
		return
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
