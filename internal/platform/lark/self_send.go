package lark

import (
	"context"
	"encoding/json"

	"github.com/go-aie/teamx/internal/platform"
)

// A separate operation keeps capability authority local to exact self delivery.
// Ordinary user sends retain their existing acceptance and authority gates.
func SelfSendAction(reader SendReader, write SendWriter) platform.ActionCapability {
	cap := SendActions(reader, write, "")[platform.OperationMessageSend]
	cap.Spec.Operation = platform.OperationMessageSendSelf
	cap.Spec.BindingEvidence = "self_delivery"
	cap.Spec.ObjectTypes = []string{"user"}
	cap.Spec.DirectRoomInstructionOnly = false
	cap.Spec.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["body"],"properties":{"body":{"type":"string","minLength":1,"maxLength":4096,"description":"发送到当前绑定用户的本人自聊；只保存明确选择的 UTF-8 纯文本，最多 4096 字节"}}}`)
	inspect, execute := cap.Inspect, cap.Execute
	cap.AppliesTo = func(t platform.TargetIdentity, a platform.AccountIdentity) bool {
		return validSendIdentity(t, a) && t.Object.Type == "user" && t.Object.ID == a.ID && len(t.Containers) == 1 && !t.Message.Valid() && !t.Discussion.Valid()
	}
	valid := func(q platform.TargetActionRequest, a platform.AccessContext) bool {
		return q.Operation == platform.OperationMessageSendSelf && validSendTarget(q.Target, a) && q.Target.Object.Type == "user" && q.Target.Object.ID == a.Account.ID && len(q.Target.Containers) == 1
	}
	cap.Inspect = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
		if !valid(q, a) {
			return platform.TargetActionInspection{}, failure("self_target_required")
		}
		q.Operation = platform.OperationMessageSend
		return inspect(ctx, q, a)
	}
	cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
		if !valid(q, a) {
			return platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "not_applied", ReasonCode: "self_target_required", RetryClass: "never"}, nil
		}
		q.Operation = platform.OperationMessageSend
		return execute(ctx, q, a)
	}
	return cap
}
