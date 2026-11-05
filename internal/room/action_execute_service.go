package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Credential resolution, process ownership and cancellation use the normal tool gate.
func (m *roomManager) actionAccountLocked(req toolsRequest) (connection.Account, error) {
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return connection.Account{}, err
	}
	file, err := m.connections.Load()
	if err != nil {
		return connection.Account{}, err
	}
	for _, b := range f.Bindings {
		if b.Tool != "gitlab" || b.AgentID != req.Caller.ID || b.MembershipID != req.Caller.MembershipID {
			continue
		}
		for _, c := range file.Connections {
			if c.ID == b.ConnectionID && c.Generation == b.ConnectionGeneration && c.Provider == "gitlab" {
				a, _, err := connection.ResolveAccount(file, c)
				return a, err
			}
		}
	}
	return connection.Account{}, ErrConflict
}

type actionInspection struct {
	Status    string `json:"status"`
	ProjectID int64  `json:"project_id"`
	MRID      int64  `json:"mr_id"`
	MRIID     int64  `json:"mr_iid"`
	Project   string `json:"project"`
	URL       string `json:"url"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
}
type actionToolResult struct {
	Status    string `json:"status"`
	Retryable bool   `json:"retryable"`
	RemoteID  string `json:"remote_id"`
	URL       string `json:"url"`
	Message   string `json:"message"`
	MRID      int64  `json:"mr_id"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
}
type actionOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *actionOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 32<<10 {
		b.overflow = true
		return n, nil
	}
	return b.Buffer.Write(p)
}
func runActionAdapter(ctx context.Context, p tool.Process, q ActionRequest, mode string, out any) bool {
	input, _ := json.Marshal(struct {
		ProjectID string `json:"project_id"`
		MRIID     string `json:"mr_iid"`
		Body      string `json:"body"`
	}{q.Target.ProjectID, q.Target.MRIID, q.Input.Body})
	var stdout actionOutput
	p.Input, p.Output, p.Error = bytes.NewReader(input), &stdout, io.Discard
	code := p.Run(ctx, []string{"__action-mr-comment", mode})
	return code == 0 && !stdout.overflow && strictToolJSON(stdout.Bytes(), out) == nil
}
func (m *roomManager) serveActionExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	var req apiRequest
	if !decodeToolRequest(w, r, &req) {
		return
	}
	if req.ActionRequest == nil || req.AgentID != "" || req.MembershipID != "" {
		writeAPIError(w, ErrRequest)
		return
	}
	result, err := m.executeAction(r.Context(), req, *req.ActionRequest)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
func (m *roomManager) executeAction(ctx context.Context, req apiRequest, q ActionRequest) (ActionReceipt, error) {
	tr := toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, Tool: "gitlab", Args: []string{"__action-mr-comment", "inspect"}}
	m.mu.Lock()
	err := m.checkToolMemberLocked(ctx, tr)
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	m.mu.Unlock()
	if err != nil {
		return ActionReceipt{}, err
	}
	id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	// Completed/uncertain IDs remain readable with their original turn/version/token,
	// even after the binding has changed or the current turn has ended.
	if prior, err := svc.store.existingAction(id, q); err != nil || prior != nil {
		if prior != nil {
			return *prior, nil
		}
		return ActionReceipt{}, err
	}
	if !validActionRequest(q) {
		return ActionReceipt{}, ErrRequest
	}
	// Reject stale rules before inspecting the remote object.
	view, err := svc.store.readAgentPolicy(id)
	if err != nil {
		return ActionReceipt{}, err
	}
	if view.PolicyToken != q.PolicyToken {
		return ActionReceipt{ID: q.ID, Status: "policy_changed", MatterVersion: q.MatterVersion, Policy: &view, Message: "规则已变化；请根据完整有效视图重新判断。"}, nil
	}
	m.mu.Lock()
	account, err := m.actionAccountLocked(tr)
	var p tool.Process
	var call *toolCall
	var toolCtx context.Context
	if err == nil {
		p, call, toolCtx, err = m.prepareToolLocked(ctx, tr)
	}
	m.mu.Unlock()
	if err != nil {
		return ActionReceipt{}, err
	}
	defer m.finishTool(call)
	var inspected actionInspection
	if !runActionAdapter(toolCtx, p, q, "inspect", &inspected) {
		return ActionReceipt{}, fmt.Errorf("%w: MR/account inspection failed; no comment dispatched", ErrRequest)
	}
	if inspected.Status != "verified" || inspected.UserID != account.UserID || inspected.Username != account.Username || inspected.MRID <= 0 ||
		strconv.FormatInt(inspected.ProjectID, 10) != q.Target.ProjectID || strconv.FormatInt(inspected.MRIID, 10) != q.Target.MRIID {
		return ActionReceipt{}, ErrConflict
	}
	target := ActionTarget{Tool: "gitlab", Provider: "gitlab", Instance: account.URL, Account: account.Username, Project: inspected.Project, ProjectID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: q.Target.ProjectID},
		Object: platform.ObjectReference{Type: "MergeRequest", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: strconv.FormatInt(inspected.MRID, 10)}, IID: platform.ExternalID{Namespace: "gitlab:rest:iid", Value: q.Target.MRIID}}, Operation: q.Operation, URL: inspected.URL}
	// Require an actual MR source; unknown provider/object identities cannot confer a target.
	svc.store.mu.Lock()
	_, record, _, sourceErr := svc.store.actionRecordLocked(id, q)
	svc.store.mu.Unlock()
	ref := record.Source.Reference
	if sourceErr != nil {
		return ActionReceipt{}, sourceErr
	}
	if ref.Version != 1 || ref.ProjectID.Value == "" || ref.Object.Type != "MergeRequest" || (ref.Object.ID.Value == "" && ref.Object.IID.Value == "") {
		return ActionReceipt{}, ErrRequest
	}
	receipt, started, err := svc.store.beginAction(id, q, target)
	if err != nil || !started {
		return receipt, err
	}
	result := ActionReceipt{Status: "not_applied", Message: "执行前停止；未发送评论。"}
	uncertain := false
	for attempt := 1; attempt <= 2; attempt++ {
		// A previous possible delivery stays unknown even when a later check/refusal
		// prevents another attempt. No error can turn that evidence into non-delivery.
		if toolCtx.Err() != nil {
			result.Message = "用户取消或服务关闭，已停止；可能已发送的评论不会自动补发。"
			break
		}
		m.mu.Lock()
		checkErr := m.checkToolMemberLocked(toolCtx, tr)
		current, accountErr := m.actionAccountLocked(tr)
		m.mu.Unlock()
		if checkErr != nil || accountErr != nil || current != account {
			result.Message = "账号或工具绑定已变化，执行已停止；请核实已有尝试结果。"
			break
		}
		latest, attemptErr := svc.store.actionAttempt(id, q)
		if attemptErr != nil || latest != nil {
			result.Message = "规则、决定或当前回合已变化，执行已停止；请重新判断并核实已有尝试结果。"
			result.Policy = latest
			break
		}
		if attempt == 2 && uncertain {
			result.PossibleDuplicate = true
		}
		var response actionToolResult
		if !runActionAdapter(toolCtx, p, q, "send", &response) {
			// A killed/missing process or malformed envelope cannot authorize a retry.
			response = actionToolResult{Status: "unknown", Message: "工具未返回可验证的业务结果。"}
		}
		if response.Status == "succeeded" && (!actionDecimal(response.RemoteID) || response.MRID != inspected.MRID || response.UserID != account.UserID || response.Username != account.Username) {
			response = actionToolResult{Status: "unknown", Message: "评论结果的对象或执行账号不匹配。"}
		}
		if response.Status != "succeeded" && response.Status != "not_applied" && response.Status != "unknown" {
			response = actionToolResult{Status: "unknown", Message: "工具返回无效结果。"}
		}
		result.Status, result.Message = response.Status, response.Message
		if !handlingText(result.Message) {
			result.Message = "工具返回结果；请检查准确动作回执。"
		}
		if response.Status == "succeeded" {
			result.RemoteID = response.RemoteID
			result.URL = inspected.URL + "#note_" + response.RemoteID
			if len(result.URL) > 4096 {
				result.URL = ""
			} // retain the exact ID rather than a truncated URL
			break
		}
		if response.Status == "unknown" {
			uncertain = true
		}
		if !response.Retryable || attempt == 2 || toolCtx.Err() != nil {
			break
		}
	}
	if uncertain && result.Status != "succeeded" {
		result.Status = "unknown"
	}
	// Leave room for outcome warnings within the existing evidence limit.
	if len(result.Message) > 3000 {
		result.Message = "工具结果消息超出上限；请以结构化回执和远端核实为准。"
	}
	if result.Status == "unknown" {
		result.Message += " 结果不确定，可能已经发送；按需查询远端并报告，不得自动重发。"
	}
	if result.PossibleDuplicate {
		result.Message += " 暂时性故障后再次尝试，可能出现重复评论。"
	}
	// Persistence is deliberately independent of request cancellation and turn
	// version: a dispatched comment must retain its actual evidence after a stop.
	receipt, err = svc.store.finishAction(id, q, result)
	if err != nil {
		return ActionReceipt{}, fmt.Errorf("%w: action result could not be saved; remote result may already exist; query records/remote, do not resend", err)
	}
	return receipt, nil
}

// Keep the CLI's outer deadline above two 30s adapter attempts plus inspection.
const actionClientTimeout = 3 * time.Minute
