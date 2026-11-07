package lark

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func replyFixture(t *testing.T) (*fakeReader, platform.AccessContext, platform.TargetActionRequest) {
	t.Helper()
	a := platform.AccessContext{RoomIdentity: platform.RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "agent", MembershipID: "member", Account: sampleBinding().Account, ConnectionID: "connection", Generation: "generation", ConnectionVersion: 1, CredentialVersion: 1}
	m := sampleMessage("om_original", "oc_chat", "收到的问题")
	receipt := sampleMessage("om_receipt", "oc_chat", "正文\n\"精确\" @./literal")
	receipt.Sender = Sender{ID: a.Account.ID.Value, IDType: "open_id", Type: "user", TenantKey: a.Account.Tenant.Value}
	receipt.ParentID, receipt.RootID = m.ID, m.ID
	f := &fakeReader{chat: Chat{ID: m.ChatID, Mode: "p2p", Name: "测试私聊", TenantKey: "external_tenant"}, messages: map[string]Message{m.ID: m, receipt.ID: receipt}}
	target := platform.TargetIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: a.Account.Instance, Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: f.chat.TenantKey}}, Containers: []platform.NativeIdentity{{Type: "chat", ID: platform.ExternalID{Namespace: nativeNamespace(a.Account, "chat_id"), Value: m.ChatID}}}, Object: platform.NativeIdentity{Type: "message", ID: platform.ExternalID{Namespace: nativeNamespace(a.Account, "message_id"), Value: m.ID}}, Message: platform.ExternalID{Namespace: nativeNamespace(a.Account, "message_id"), Value: m.ID}}
	input, _ := json.Marshal(ReplyInput{Body: "正文\n\"精确\" @./literal", ReplyMode: "chat"})
	return f, a, platform.TargetActionRequest{Target: target, Operation: platform.OperationMessageReply, Input: input, IdempotencyKey: strings.Repeat("a", 32)}
}

func TestReplyAdapterExactTargetAndReceipt(t *testing.T) {
	for _, mode := range []string{"p2p", "group"} {
		t.Run(mode, func(t *testing.T) {
			f, a, q := replyFixture(t)
			f.chat.Mode = mode
			calls := 0
			cap := ReplyActions(f, func(ctx context.Context, id, body, key string) (string, error) {
				calls++
				if id != q.Target.Message.Value || body != "正文\n\"精确\" @./literal" || key != q.IdempotencyKey {
					t.Fatal("dispatch changed identity/input")
				}
				return "om_receipt", nil
			}, "")[platform.OperationMessageReply]
			inspection, err := cap.Inspect(context.Background(), q, a)
			if err != nil || inspection.ContainerLabel != f.chat.Name {
				t.Fatal(inspection, err)
			}
			r, err := cap.Execute(context.Background(), q, a)
			if err != nil || r.Status != "succeeded" || r.Account != a.Account || r.ResultID.Value != "om_receipt" || calls != 1 || cap.Spec.Retry.MaxAttempts != 1 || cap.Spec.Retry.RetryUnknown || !cap.Spec.RequiresAuthorization {
				t.Fatal(r, err, calls)
			}
			if Matches(f.messages[r.ResultID.Value], f.chat, a.Account.ID.Value, Config{Private: true, GroupMentions: true, AtAll: true}) {
				t.Fatal("self-produced reply reentered notifications")
			}
		})
	}
}

func TestReplyAdapterRejectsBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"disabled", "thread", "topic", "unknown_field", "empty", "oversize", "invalid_utf8", "operation", "provider", "app", "account", "chat", "tenant", "deleted", "root_missing", "parent_missing", "cycle", "key"} {
		t.Run(mode, func(t *testing.T) {
			f, a, q := replyFixture(t)
			disabled := ""
			switch mode {
			case "disabled":
				disabled = ReplyDisabledReason
			case "thread":
				q.Input = json.RawMessage(`{"body":"hello","reply_mode":"thread"}`)
			case "topic":
				f.chat.Mode = "topic"
			case "unknown_field":
				q.Input = json.RawMessage(`{"body":"hello","reply_mode":"chat","chat_id":"oc_other"}`)
			case "empty":
				q.Input = json.RawMessage(`{"body":"  ","reply_mode":"chat"}`)
			case "oversize":
				q.Input, _ = json.Marshal(ReplyInput{Body: strings.Repeat("中", 1366), ReplyMode: "chat"})
			case "invalid_utf8":
				q.Input = append([]byte(`{"body":"`), append([]byte{255}, []byte(`","reply_mode":"chat"}`)...)...)
			case "operation":
				q.Operation = platform.OperationObjectComment
			case "provider":
				q.Target.Provider = "gitlab"
			case "app":
				q.Target.Message.Namespace = "lark:message_id:cli_other"
			case "account":
				a.Account.ID.Namespace = "lark:open_id:cli_other"
			case "chat":
				q.Target.Containers[0].ID.Value = "oc_other"
			case "tenant":
				q.Target.Tenant.Value = "other"
			case "deleted":
				m := f.messages["om_original"]
				m.Deleted = true
				f.messages[m.ID] = m
			case "root_missing":
				m := f.messages["om_original"]
				m.RootID = "om_missing"
				m.ParentID = "om_missing"
				f.messages[m.ID] = m
				q.Target.Object.ID.Value = m.RootID
			case "parent_missing":
				m := f.messages["om_original"]
				m.ParentID = "om_missing"
				f.messages[m.ID] = m
			case "cycle":
				m := f.messages["om_original"]
				m.RootID = "om_root"
				m.ParentID = m.ID
				f.messages[m.ID] = m
				q.Target.Object.ID.Value = m.RootID
			case "key":
				q.IdempotencyKey = ""
			}
			calls := 0
			cap := ReplyActions(f, func(context.Context, string, string, string) (string, error) { calls++; return "om_receipt", nil }, disabled)[platform.OperationMessageReply]
			r, err := cap.Execute(context.Background(), q, a)
			if err != nil || r.Status != "not_applied" || calls != 0 {
				t.Fatal(r, err, calls)
			}
		})
	}
}

func TestReplyAdapterUnknownNeverGuessesNotApplied(t *testing.T) {
	for _, mode := range []string{"timeout", "cancelled", "rate_limited", "malformed", "read_missing", "body", "author", "sender_type", "tenant", "chat", "parent", "root", "thread", "deleted", "same_id", "not_sent"} {
		t.Run(mode, func(t *testing.T) {
			f, a, q := replyFixture(t)
			m := f.messages["om_receipt"]
			switch mode {
			case "body":
				m.Body.Content = `{"text":"changed"}`
			case "author":
				m.Sender.ID = "ou_other"
			case "sender_type":
				m.Sender.Type = "app"
			case "tenant":
				m.Sender.TenantKey = "other"
			case "chat":
				m.ChatID = "oc_other"
			case "parent":
				m.ParentID = "om_other"
			case "root":
				m.RootID = "om_other"
			case "thread":
				m.ThreadID = "omt_other"
			case "deleted":
				m.Deleted = true
			}
			f.messages[m.ID] = m
			if mode == "read_missing" {
				delete(f.messages, m.ID)
			}
			calls := 0
			cap := ReplyActions(f, func(context.Context, string, string, string) (string, error) {
				calls++
				switch mode {
				case "timeout":
					return "", context.DeadlineExceeded
				case "cancelled":
					return "", context.Canceled
				case "rate_limited":
					return "", failure("rate_limited")
				case "malformed":
					return "", errors.New("broken response")
				case "same_id":
					return "om_original", nil
				case "not_sent":
					return "", ErrReplyNotSent
				}
				return m.ID, nil
			}, "")[platform.OperationMessageReply]
			r, err := cap.Execute(context.Background(), q, a)
			want := "unknown"
			if mode == "not_sent" {
				want = "not_applied"
			}
			if err != nil || r.Status != want || r.RetryClass != "never" || calls != 1 {
				t.Fatal(r, err, calls)
			}
			if mode == "read_missing" && r.ResultID.Value != m.ID {
				t.Fatal("known ID lost", r)
			}
		})
	}
}

func TestReplyTargetResolutionKeepsRootAndExactMessage(t *testing.T) {
	f, a, q := replyFixture(t)
	root := sampleMessage("om_root", "oc_chat", "root")
	m := f.messages[q.Target.Message.Value]
	m.RootID, m.ParentID = root.ID, root.ID
	f.messages[m.ID] = m
	f.messages[root.ID] = root
	q.Target.Object.ID.Value = root.ID
	r, err := ResolveReplyTarget(context.Background(), platform.TargetLocator{Candidate: &q.Target}, a, f)
	if err != nil || !r.Complete || !r.ObjectVerified || !r.MessageVerified || r.Target.Object.ID.Value != root.ID || r.Target.Message.Value != m.ID {
		t.Fatal(r, err)
	}
	root.Deleted = true
	f.messages[root.ID] = root
	r, err = ResolveReplyTarget(context.Background(), platform.TargetLocator{Candidate: &q.Target}, a, f)
	if err != nil || r.Complete || r.Status == platform.ResolutionResolved {
		t.Fatal("deleted root admitted", r, err)
	}
}
