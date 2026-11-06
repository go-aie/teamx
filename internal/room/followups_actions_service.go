package room

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/go-aie/teamx/internal/platform"
)

type referenceActionSession struct {
	Access       platform.AccessContext
	Capabilities platform.ReferenceCapabilities
	Check        func(context.Context) error
	Close        func()
}
type referenceActionPreparer func(context.Context, MemberIdentity, platform.AccessContext) (referenceActionSession, error)

func mustCommentJSON(input CommentInput) json.RawMessage { b, _ := json.Marshal(input); return b }

// Managed execution shares member binding admission and platform call ownership.
func (m *roomManager) executeReferenceAction(ctx context.Context, req apiRequest, q referenceActionRequest) (referenceActionReceipt, error) {
	tr := toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}
	m.mu.Lock()
	err := m.checkToolMemberLocked(ctx, tr)
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	m.mu.Unlock()
	if err != nil {
		return referenceActionReceipt{}, err
	}
	if req.AgentID != "" || req.MembershipID != "" {
		return referenceActionReceipt{}, ErrIdentity
	}
	id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	return svc.executeReferenceAction(ctx, id, q, func(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
		m.mu.Lock()
		current, _ := m.runtimeServiceLocked(req.RoomIdentity)
		err := m.checkToolMemberLocked(ctx, tr)
		prepare := m.referenceExecutors[old.Account.Provider]
		m.mu.Unlock()
		if err != nil {
			return referenceActionSession{}, err
		}
		if current != svc {
			return referenceActionSession{}, ErrIdentity
		}
		if prepare == nil {
			return referenceActionSession{}, platform.ErrReference
		}
		return prepare(ctx, id, old)
	})
}

func validReferenceSpec(spec platform.ActionSpec, q referenceActionRequest, t platform.TargetIdentity) bool {
	kind := "object"
	if t.Discussion.Valid() {
		kind = "discussion"
	}
	if (len(spec.ObjectTypes) > 0 && !slices.Contains(spec.ObjectTypes, t.Object.Type)) || spec.Operation != q.Operation || spec.TargetKind != kind || !json.Valid(spec.InputSchema) || spec.Retry.MaxAttempts < 1 || spec.Retry.MaxAttempts > 2 || t.Message.Valid() {
		return false
	}
	if q.Operation == platform.OperationDiscussionReply {
		return kind == "discussion" && spec.Retry.MaxAttempts == 1 && !spec.Retry.RetryUnknown
	}
	return kind == "object"
}

func (s *service) executeReferenceAction(ctx context.Context, id MemberIdentity, q referenceActionRequest, prepare referenceActionPreparer) (referenceActionReceipt, error) {
	// Manager action HTTP bypasses service.ServeHTTP's mutation gate. Keep the
	// Store owned through response processing, durable settlement and session.Close.
	s.changes.RLock()
	defer s.changes.RUnlock()
	if s.closing.Load() {
		return referenceActionReceipt{}, ErrClosing
	}
	if s.restarting.Load() {
		return referenceActionReceipt{}, ErrBusy
	}
	if prior, err := s.store.existingReferenceAction(id, q); err != nil || prior != nil {
		if prior != nil {
			return *prior, nil
		}
		return referenceActionReceipt{}, err
	}
	if err := referenceActionRequestError(q); err != nil {
		return referenceActionReceipt{}, err
	}
	view, err := s.store.readAgentPolicy(id)
	if err != nil {
		return referenceActionReceipt{}, err
	}
	if view.PolicyToken != q.PolicyToken {
		return referenceActionReceipt{ID: q.ID, Status: "policy_changed", Policy: &view}, nil
	}
	request, old, err := s.store.referenceActionEvidence(id, q)
	if err != nil {
		return referenceActionReceipt{}, err
	}
	session, err := prepare(ctx, id, old)
	if err != nil {
		return referenceActionReceipt{}, err
	}
	defer session.Close()
	access := session.Access
	// Preserve the existing receiving/executing account equality contract. Credential
	// rotations require fresh inspection, and never rename the stored target.
	if !access.Valid() || access.RoomIdentity != id.RoomIdentity || access.AgentID != id.AgentID || access.MembershipID != id.MembershipID || access.Account != old.Account || access.Receiver != old.Receiver || access.Receiver != access.Account {
		return referenceActionReceipt{}, ErrIdentity
	}
	cap, ok := session.Capabilities.Actions[q.Operation]
	if !ok || cap.Inspect == nil || cap.Execute == nil || !validReferenceSpec(cap.Spec, q, request.Target) {
		return referenceActionReceipt{}, platform.ErrReference
	}
	inspect := func() (platform.TargetActionInspection, error) {
		if err := ctx.Err(); err != nil {
			return platform.TargetActionInspection{}, err
		}
		if err := session.Check(ctx); err != nil {
			return platform.TargetActionInspection{}, err
		}
		i, err := cap.Inspect(ctx, request, access)
		if err != nil {
			return i, err
		}
		if !sameNativeTarget(i.Target, request.Target) || i.Account != access.Account || len(i.AccountLabel) > 4096 || len(i.ContainerLabel) > 4096 || len(i.URL) > 4096 {
			return i, ErrConflict
		}
		return i, session.Check(ctx)
	}
	inspected, err := inspect()
	if err != nil {
		return referenceActionReceipt{}, err
	}
	receipt, started, err := s.store.beginReferenceAction(id, q, request, access, inspected, cap.Spec)
	if err != nil || !started {
		return receipt, err
	}
	result := platform.TargetActionResult{Target: request.Target, Account: access.Account, Status: "not_applied", Message: "执行前停止，未发送。", RetryClass: "never"}
	uncertain := false
	for attempt := 1; attempt <= cap.Spec.Retry.MaxAttempts; attempt++ {
		if attempt > 1 {
			again, err := inspect()
			if err != nil || again.AccountLabel != inspected.AccountLabel || again.ContainerLabel != inspected.ContainerLabel {
				result.Message = "当前目标或账号核验失败；核实已有尝试结果。"
				break
			}
		}
		if ctx.Err() != nil || session.Check(ctx) != nil {
			result.Message = "取消或绑定变化；核实已有尝试结果。"
			break
		}
		latest, attemptErr := s.store.referenceActionAttempt(id, q, attempt > 1 && uncertain)
		if attemptErr != nil || latest != nil {
			result.Message = "规则、指令或回合已变化；核实已有尝试结果。"
			receipt.Policy = latest
			break
		}
		response, err := cap.Execute(ctx, request, access)
		if err != nil || !sameNativeTarget(response.Target, request.Target) || response.Account != access.Account || (response.Status != "succeeded" && response.Status != "not_applied" && response.Status != "unknown") || len(response.Message) > 4096 || len(response.URL) > 4096 || (response.Status == "succeeded" && !response.ResultID.Valid()) {
			response = platform.TargetActionResult{Target: request.Target, Account: access.Account, Status: "unknown", Message: "动作结果无法核实目标、账号或业务结果；不得自动重发。", RetryClass: "never"}
		}
		result = response
		if response.Status == "succeeded" {
			break
		}
		if response.Status == "unknown" {
			uncertain = true
		}
		if response.RetryClass != "transient" || (uncertain && !cap.Spec.Retry.RetryUnknown) {
			break
		}
	}
	if uncertain && result.Status != "succeeded" {
		result.Status = "unknown"
	}
	if result.Status == "unknown" {
		result.Message = "结果不确定，可能已发送；查询核实，不得自动重发。"
	}
	// Completion is independent of the cancelled request, but still obeys Store's
	// commit-unknown freeze. No next-turn/restart replay exists.
	final, err := s.store.finishReferenceAction(id, q, result)
	if err != nil {
		return referenceActionReceipt{}, fmt.Errorf("%w: action outcome may exist; reload/query before any resend", err)
	}
	final.Policy = receipt.Policy
	return final, nil
}
