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

// Explicit closed-readiness reason for isolated negative contract checks.
// The production registry publishes AcceptedActionCapabilities.
const ReplyDisabledReason = "lark_reply_acceptance_pending: 用户身份普通回复及真实 Agent 验收尚未完成，生产回复关闭。"

var ErrReplyNotSent = errors.New("lark reply request was not dispatched")

type ReplyInput struct {
	Body      string `json:"body"`
	ReplyMode string `json:"reply_mode"`
}

// ReplyWriter makes at most one fixed reply POST. Only ErrReplyNotSent proves
// no dispatch; all other failures may have applied remotely.
type ReplyWriter func(context.Context, string, string, string) (string, error)

func replyInput(raw json.RawMessage) (ReplyInput, error) {
	var input ReplyInput
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !utf8.Valid(raw) || d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !utf8.ValidString(input.Body) || strings.TrimSpace(input.Body) == "" || len(input.Body) > 4096 || strings.ContainsRune(input.Body, 0) {
		return input, failure("invalid_reply_input")
	}
	// Thread semantics need separate native acceptance; never fall back to chat.
	if input.ReplyMode != "chat" {
		return input, failure("reply_mode_unavailable")
	}
	return input, nil
}

// ReplyActions is the provider adapter. Readiness is supplied by its owner;
// production selects accepted modes through AcceptedActionCapabilities.
func ReplyActions(reader MessageReader, write ReplyWriter, disabledReason string) map[platform.Operation]platform.ActionCapability {
	spec := platform.ActionSpec{DisabledReason: disabledReason, RequiresAuthorization: true, Operation: platform.OperationMessageReply, TargetKind: "message", ObjectTypes: []string{"message"}, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["body","reply_mode"],"properties":{"body":{"type":"string","minLength":1,"maxLength":4096,"description":"非空 UTF-8 纯文本，最多 4096 字节"},"reply_mode":{"type":"string","enum":["chat"],"description":"普通消息回复；话题回复尚未验收，不可用"}}}`), Retry: platform.RetryPolicy{MaxAttempts: 1, RetryUnknown: false}}
	inspect := func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
		if disabledReason != "" {
			return platform.TargetActionInspection{}, failure("reply_acceptance_pending")
		}
		if q.Operation != platform.OperationMessageReply {
			return platform.TargetActionInspection{}, failure("operation_mismatch")
		}
		if _, err := replyInput(q.Input); err != nil {
			return platform.TargetActionInspection{}, err
		}
		message, chat, err := verifyReplyTarget(ctx, q.Target, a, reader)
		if err != nil {
			return platform.TargetActionInspection{}, err
		}
		// A thread-only chat/target cannot be silently answered in the main chat.
		if chat.Mode == "topic" || message.ThreadID != "" {
			return platform.TargetActionInspection{}, failure("reply_mode_unavailable")
		}
		own := message.Sender.Type == "user" && message.Sender.IDType == "open_id" && message.Sender.ID == a.Account.ID.Value
		return platform.TargetActionInspection{Target: q.Target, Account: a.Account, ContainerLabel: chat.Name, ContextOnly: own}, nil
	}
	return map[platform.Operation]platform.ActionCapability{platform.OperationMessageReply: {Spec: spec, Inspect: inspect, Execute: func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
		result := platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "not_applied", RetryClass: "never"}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := inspect(ctx, q, a); err != nil {
			result.ReasonCode = ErrorCode(err)
			return result, nil
		}
		if write == nil || len(q.IdempotencyKey) != 32 || strings.Trim(q.IdempotencyKey, "0123456789abcdef") != "" {
			result.ReasonCode = "invalid_reply_dispatch"
			return result, nil
		}
		input, _ := replyInput(q.Input)
		id, err := write(ctx, q.Target.Message.Value, input.Body, q.IdempotencyKey)
		if errors.Is(err, ErrReplyNotSent) {
			result.ReasonCode = "reply_not_sent"
			return result, nil
		}
		result.Status = "unknown"
		if nativeID(id, "om_") {
			result.ResultID = platform.ExternalID{Namespace: nativeNamespace(a.Account, "message_id"), Value: id}
		}
		if err != nil || !result.ResultID.Valid() {
			result.ReasonCode = "reply_result_unknown"
			return result, nil
		}
		// Exact-ID independent read: matching text/search alone never proves success.
		m, err := reader.Message(ctx, a.Account, id)
		var body struct {
			Text string `json:"text"`
		}
		root := q.Target.Object.ID.Value
		if err != nil || !m.valid() || m.Deleted || m.ID != id || id == q.Target.Message.Value || m.ChatID != q.Target.Containers[0].ID.Value || m.Type != "text" || m.Sender.Type != "user" || m.Sender.IDType != "open_id" || m.Sender.ID != a.Account.ID.Value || m.Sender.TenantKey != a.Account.Tenant.Value || m.ParentID != q.Target.Message.Value || m.RootID != root || m.ThreadID != "" || decode([]byte(m.Body.Content), &body) != nil || body.Text != input.Body {
			result.ReasonCode = "reply_verification_failed"
			return result, nil
		}
		result.Status = "succeeded"
		return result, nil
	}}}
}

func validMessageTarget(t platform.TargetIdentity, a platform.AccessContext) bool {
	return a.Valid() && a.Account.Provider == "lark" && t.Valid() && t.Provider == "lark" && t.Instance == a.Account.Instance && len(t.Containers) == 1 && t.Containers[0].Type == "chat" && t.Containers[0].ID.Namespace == nativeNamespace(a.Account, "chat_id") && nativeID(t.Containers[0].ID.Value, "oc_") && t.Object.Type == "message" && t.Object.ID.Namespace == nativeNamespace(a.Account, "message_id") && nativeID(t.Object.ID.Value, "om_") && t.Message.Namespace == t.Object.ID.Namespace && nativeID(t.Message.Value, "om_") && (!t.Discussion.Valid() || t.Discussion.Namespace == nativeNamespace(a.Account, "thread_id") && nativeID(t.Discussion.Value, "omt_"))
}

// Re-read the exact message, chat, root and parent, without regrouping a saved
// target on failure. The bounded chain rejects cycles and incomplete relations.
func verifyReplyTarget(ctx context.Context, t platform.TargetIdentity, a platform.AccessContext, reader MessageReader) (Message, Chat, error) {
	if reader == nil || !validMessageTarget(t, a) {
		return Message{}, Chat{}, failure("target_mismatch")
	}
	m, err := reader.Message(ctx, a.Account, t.Message.Value)
	if err != nil {
		return Message{}, Chat{}, err
	}
	chat, err := reader.Chat(ctx, a.Account, t.Containers[0].ID.Value)
	if err != nil {
		return Message{}, Chat{}, err
	}
	tenant := platform.ExternalID{}
	if chat.TenantKey != "" {
		tenant = platform.ExternalID{Namespace: "lark:tenant_key", Value: chat.TenantKey}
	}
	if !m.valid() || m.Deleted || m.ID != t.Message.Value || m.ChatID != chat.ID || !chat.Valid() || chat.ID != t.Containers[0].ID.Value || tenant != t.Tenant || m.Type == "system" || (m.ThreadID == "") != !t.Discussion.Valid() || t.Discussion.Valid() && t.Discussion.Value != m.ThreadID {
		return Message{}, Chat{}, failure("target_mismatch")
	}
	root := m.ID
	if m.RootID != "" {
		root = m.RootID
	}
	if root != t.Object.ID.Value || m.ParentID != "" && m.RootID == "" {
		return Message{}, Chat{}, failure("root_mismatch")
	}
	current := m
	seen := map[string]bool{m.ID: true}
	for depth := 0; current.ID != root; depth++ {
		if depth >= 16 || current.ParentID == "" || seen[current.ParentID] {
			return Message{}, Chat{}, failure("parent_unavailable")
		}
		parent, e := reader.Message(ctx, a.Account, current.ParentID)
		if e != nil {
			return Message{}, Chat{}, e
		}
		if !parent.valid() || parent.Deleted || parent.ID != current.ParentID || parent.ChatID != m.ChatID || (parent.ID != root && parent.RootID != root) || (parent.ThreadID != "" && parent.ThreadID != m.ThreadID) {
			return Message{}, Chat{}, failure("parent_mismatch")
		}
		seen[parent.ID] = true
		current = parent
	}
	if current.ParentID != "" || current.RootID != "" && current.RootID != current.ID {
		return Message{}, Chat{}, failure("root_mismatch")
	}
	return m, chat, nil
}

// ResolveReplyTarget only accepts message locators. Users/chats/new messages
// belong to L4-C; a candidate is verified input, never an execution shortcut.
func ResolveReplyTarget(ctx context.Context, l platform.TargetLocator, a platform.AccessContext, reader MessageReader) (platform.TargetResolution, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r := platform.TargetResolution{Status: platform.ResolutionUnsupported, ReasonCode: "message_locator_required", Account: a.Account, CheckedAt: time.Now().UTC()}
	if l.Query != nil || l.URL != "" || l.TargetRef != "" || (l.Source == nil) == (l.Candidate == nil) {
		return r, nil
	}
	if l.Source != nil {
		var err error
		r, err = ResolveTarget(ctx, l, a, reader)
		if err != nil || r.Status != platform.ResolutionResolved || !r.Complete || r.Target == nil {
			return r, err
		}
	} else {
		target := *l.Candidate
		r.Target = &target
	}
	_, chat, err := verifyReplyTarget(ctx, *r.Target, a, reader)
	if err != nil {
		r.Status, r.ReasonCode, r.Complete = platform.ResolutionUnavailable, ErrorCode(err), false
		return r, nil
	}
	r.Status, r.ReasonCode, r.Complete = platform.ResolutionResolved, "", true
	r.TargetLabel = chat.Name
	r.ObjectVerified, r.MessageVerified, r.DiscussionVerified = true, true, r.Target.Discussion.Valid()
	return r, nil
}

func ReplyCapabilities(reader MessageReader, write ReplyWriter, disabledReason string) platform.ReferenceCapabilities {
	caps := Capabilities(reader)
	caps.Resolve = func(ctx context.Context, l platform.TargetLocator, a platform.AccessContext) (platform.TargetResolution, error) {
		return ResolveReplyTarget(ctx, l, a, reader)
	}
	caps.Actions = ReplyActions(reader, write, disabledReason)
	return caps
}
