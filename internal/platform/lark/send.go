package lark

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
)

const SendDisabledReason = "lark_send_acceptance_pending: 主动私信的完整 Agent 链路尚未完成验收，暂未开放。"

var ErrSendNotSent = errors.New("lark send request was not dispatched")

type SendInput struct {
	Body string `json:"body"`
}

// The verified target determines receive_id_type. The writer never accepts
// agent-supplied CLI arguments or an alternate destination on failure.
type SendWriter func(context.Context, platform.TargetIdentity, string, string) (string, error)

func sendInput(raw json.RawMessage) (SendInput, error) {
	var input SendInput
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !utf8.Valid(raw) || d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !utf8.ValidString(input.Body) || strings.TrimSpace(input.Body) == "" || len(input.Body) > 4096 || strings.ContainsRune(input.Body, 0) {
		return input, failure("invalid_send_input")
	}
	return input, nil
}
func SendActions(reader SendReader, write SendWriter, disabled string) map[platform.Operation]platform.ActionCapability {
	spec := platform.ActionSpec{Operation: platform.OperationMessageSend, TargetKind: "object", ObjectTypes: []string{"user", "chat"}, RequiresAuthorization: true, DirectRoomInstructionOnly: true, DisabledReason: disabled, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["body"],"properties":{"body":{"type":"string","minLength":1,"maxLength":4096,"description":"非空 UTF-8 纯文本，最多 4096 字节；一次发送一个准确用户或单群"}}}`), Retry: platform.RetryPolicy{MaxAttempts: 1, RetryUnknown: false}}
	inspect := func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
		if disabled != "" {
			return platform.TargetActionInspection{}, failure("send_acceptance_pending")
		}
		if q.Operation != platform.OperationMessageSend {
			return platform.TargetActionInspection{}, failure("operation_mismatch")
		}
		if _, err := sendInput(q.Input); err != nil {
			return platform.TargetActionInspection{}, err
		}
		_, name, err := verifySendTarget(ctx, q.Target, a, reader)
		if err != nil {
			return platform.TargetActionInspection{}, err
		}
		if q.Target.Object.Type == "user" && len(q.Target.Containers) != 1 {
			return platform.TargetActionInspection{}, failure("peer_unverified")
		}
		return platform.TargetActionInspection{Target: q.Target, Account: a.Account, ContainerLabel: name}, nil
	}
	return map[platform.Operation]platform.ActionCapability{platform.OperationMessageSend: {Spec: spec, Inspect: inspect, Execute: func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
		r := platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "not_applied", RetryClass: "never"}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := inspect(ctx, q, a); err != nil {
			r.ReasonCode = ErrorCode(err)
			return r, nil
		}
		if write == nil || len(q.IdempotencyKey) != 32 || strings.Trim(q.IdempotencyKey, "0123456789abcdef") != "" {
			r.ReasonCode = "invalid_send_dispatch"
			return r, nil
		}
		input, _ := sendInput(q.Input)
		id, err := write(ctx, q.Target, input.Body, q.IdempotencyKey)
		if errors.Is(err, ErrSendNotSent) {
			r.ReasonCode = "send_not_sent"
			return r, nil
		}
		r.Status = "unknown"
		if nativeID(id, "om_") {
			r.ResultID = platform.ExternalID{Namespace: nativeNamespace(a.Account, "message_id"), Value: id}
		}
		if err != nil || !r.ResultID.Valid() {
			r.ReasonCode = "send_result_unknown"
			return r, nil
		}
		message, e := reader.Message(ctx, a.Account, id)
		chatID := q.Target.Object.ID.Value
		if q.Target.Object.Type == "user" {
			chatID = q.Target.Containers[0].ID.Value
		}
		// Message wire uses {text}, whereas action input uses {body}.
		var text struct {
			Text string `json:"text"`
		}
		if e != nil || !message.valid() || message.ID != id || message.Deleted || message.ChatID != chatID || message.Type != "text" || message.ParentID != "" || message.RootID != "" || message.ThreadID != "" || message.Sender.Type != "user" || message.Sender.IDType != "open_id" || message.Sender.ID != a.Account.ID.Value || message.Sender.TenantKey != a.Account.Tenant.Value || decode([]byte(message.Body.Content), &text) != nil || text.Text != input.Body {
			r.ReasonCode = "send_verification_failed"
			return r, nil
		}
		r.Status = "succeeded"
		return r, nil
	}}}
}

// AcceptedActionCapabilities publishes only modes with real managed Agent
// acceptance: ordinary replies and single-group sends. User sends remain absent
// from the public schema and are also rejected before any adapter read/write.
func AcceptedActionCapabilities(reader SendReader, reply ReplyWriter, send SendWriter) platform.ReferenceCapabilities {
	caps := ActionCapabilities(reader, reply, send, "", "")
	cap := caps.Actions[platform.OperationMessageSend]
	cap.Spec.ObjectTypes = []string{"chat"}
	inspect, execute := cap.Inspect, cap.Execute
	cap.Inspect = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
		if q.Target.Object.Type != "chat" {
			return platform.TargetActionInspection{}, failure("user_send_acceptance_pending")
		}
		return inspect(ctx, q, a)
	}
	cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
		if q.Target.Object.Type != "chat" {
			return platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "not_applied", ReasonCode: "user_send_acceptance_pending", RetryClass: "never"}, nil
		}
		return execute(ctx, q, a)
	}
	caps.Actions[platform.OperationMessageSend] = cap
	return caps
}

func ActionCapabilities(reader SendReader, reply ReplyWriter, send SendWriter, replyDisabled, sendDisabled string) platform.ReferenceCapabilities {
	caps := ReplyCapabilities(reader, reply, replyDisabled)
	resolve := caps.Resolve
	caps.Resolve = func(ctx context.Context, l platform.TargetLocator, a platform.AccessContext) (platform.TargetResolution, error) {
		if l.Query != nil || l.Candidate != nil && (l.Candidate.Object.Type == "user" || l.Candidate.Object.Type == "chat") {
			return ResolveSendTarget(ctx, l, a, reader)
		}
		return resolve(ctx, l, a)
	}
	for op, cap := range SendActions(reader, send, sendDisabled) {
		caps.Actions[op] = cap
	}
	return caps
}

func (p *Pinned) SendText(ctx context.Context, target platform.TargetIdentity, body, key string, guard func(context.Context) error) (string, error) {
	if p == nil || p.client == nil || guard == nil {
		return "", ErrSendNotSent
	}

	input, _ := json.Marshal(SendInput{Body: body})
	if _, err := sendInput(input); err != nil || !utf8.ValidString(body) || !validSendIdentity(target, p.Account) || target.Object.ID == p.Account.ID || len(key) != 32 || strings.Trim(key, "0123456789abcdef") != "" || target.Object.Type == "user" && len(target.Containers) != 1 {
		return "", ErrSendNotSent
	}
	permission, err := p.SendPermission(ctx)
	if err != nil || permission.UserScopes != "granted" || permission.ApplicationScopes != "granted" {
		return "", ErrSendNotSent
	}
	c := p.client
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", ErrSendNotSent
	}
	defer unlock()
	validation, err := c.verify(ctx)
	if err != nil || validation.Account != p.Account {
		return "", ErrSendNotSent
	}
	ctx = context.WithValue(ctx, accountContextKey{}, p.Account.Instance+"|"+p.Account.ID.Namespace+"|"+p.Account.ID.Value)
	release, err := c.requests.acquire(ctx)
	if err != nil {
		return "", ErrSendNotSent
	}
	defer release()
	kind := "chat_id"
	if target.Object.Type == "user" {
		kind = "open_id"
	}
	params, _ := json.Marshal(map[string]string{"receive_id_type": kind})
	content, _ := json.Marshal(map[string]string{"text": body})
	payload, _ := json.Marshal(map[string]string{"receive_id": target.Object.ID.Value, "msg_type": "text", "content": string(content), "uuid": key})
	if ctx.Err() != nil || guard(ctx) != nil {
		return "", ErrSendNotSent
	}
	data, err := c.run(ctx, "api", "POST", "/open-apis/im/v1/messages", "--params", string(params), "--data", string(payload), "--as", "user", "--format", "json")
	if err != nil {
		return "", err
	}
	var wire struct {
		OK       bool   `json:"ok"`
		Identity string `json:"identity"`
		Data     struct {
			ID string `json:"message_id"`
		} `json:"data"`
	}
	if decode(data, &wire) != nil || !wire.OK || wire.Identity != "user" || !nativeID(wire.Data.ID, "om_") {
		return "", failure("invalid_response")
	}
	return wire.Data.ID, nil
}
