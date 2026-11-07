package lark

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

type sendTestReader struct {
	*fakeReader
	peer, group Chat
	peerID      string
	denied      bool
}

func (f *sendTestReader) DirectUser(_ context.Context, a platform.AccountIdentity, id string) (Chat, string, error) {
	if f.denied || id != f.peerID {
		return Chat{}, "", errors.New("peer unavailable")
	}
	return f.peer, "同名用户", nil
}
func (f *sendTestReader) SendGroup(_ context.Context, a platform.AccountIdentity, id string) (Chat, error) {
	if f.denied || id != f.group.ID {
		return Chat{}, errors.New("membership unavailable")
	}
	return f.group, nil
}
func (f *sendTestReader) FindSendTargets(_ context.Context, a platform.AccountIdentity, q platform.TargetQuery) ([]platform.TargetCandidate, bool, error) {
	t := platform.TargetIdentity{IdentityScope: a.IdentityScope, Object: platform.NativeIdentity{Type: "user", ID: platform.ExternalID{Namespace: a.ID.Namespace, Value: "ou_peer"}}}
	other := t
	other.Object.ID.Value = "ou_other"
	return []platform.TargetCandidate{{Target: t, Label: "同名用户"}, {Target: other, Label: "同名用户"}}, true, nil
}
func sendFixture(t *testing.T, kind string) (*sendTestReader, platform.AccessContext, platform.TargetActionRequest) {
	f, a, _ := replyFixture(t)
	reader := &sendTestReader{fakeReader: f, peer: Chat{ID: "oc_private", Mode: "p2p", TenantKey: a.Account.Tenant.Value}, group: Chat{ID: "oc_group", Mode: "group", TenantKey: a.Account.Tenant.Value, Name: "测试群"}, peerID: "ou_peer"}
	target := platform.TargetIdentity{IdentityScope: a.Account.IdentityScope, Object: platform.NativeIdentity{Type: kind}}
	chat := "oc_group"
	if kind == "user" {
		target.Object.ID = platform.ExternalID{Namespace: a.Account.ID.Namespace, Value: reader.peerID}
		target.Containers = []platform.NativeIdentity{{Type: "chat", ID: platform.ExternalID{Namespace: nativeNamespace(a.Account, "chat_id"), Value: reader.peer.ID}}}
		chat = reader.peer.ID
	} else {
		target.Object.ID = platform.ExternalID{Namespace: nativeNamespace(a.Account, "chat_id"), Value: reader.group.ID}
	}
	m := sampleMessage("om_receipt", chat, "精确正文\n\"test\" @./literal")
	m.Sender = Sender{ID: a.Account.ID.Value, IDType: "open_id", Type: "user", TenantKey: a.Account.Tenant.Value}
	f.messages[m.ID] = m
	input, _ := json.Marshal(SendInput{Body: "精确正文\n\"test\" @./literal"})
	return reader, a, platform.TargetActionRequest{Target: target, Operation: platform.OperationMessageSend, Input: input, IdempotencyKey: strings.Repeat("a", 32)}
}

func TestAcceptedActionsSeparateGroupSendsFromPendingUserSends(t *testing.T) {
	for _, kind := range []string{"user", "chat"} {
		t.Run(kind, func(t *testing.T) {
			reader, access, request := sendFixture(t, kind)
			calls := 0
			caps := AcceptedActionCapabilities(reader, nil, func(context.Context, platform.TargetIdentity, string, string) (string, error) {
				calls++
				return "om_receipt", nil
			})
			cap := caps.Actions[platform.OperationMessageSend]
			if cap.Spec.DisabledReason != "" || len(cap.Spec.ObjectTypes) != 1 || cap.Spec.ObjectTypes[0] != "chat" || caps.Actions[platform.OperationMessageReply].Spec.DisabledReason != "" {
				t.Fatal("accepted mode descriptions diverged", caps)
			}
			_, inspected := cap.Inspect(context.Background(), request, access)
			result, err := cap.Execute(context.Background(), request, access)
			if kind == "user" {
				if ErrorCode(inspected) != "user_send_acceptance_pending" || err != nil || result.Status != "not_applied" || result.ReasonCode != "user_send_acceptance_pending" || calls != 0 {
					t.Fatal("unaccepted user send reached writer", result, inspected, err, calls)
				}
			} else if inspected != nil || err != nil || result.Status != "succeeded" || calls != 1 {
				t.Fatal("accepted group send failed", result, inspected, err, calls)
			}
		})
	}
}

func TestSendAdapterAndResolution(t *testing.T) {
	for _, kind := range []string{"user", "chat"} {
		t.Run(kind, func(t *testing.T) {
			f, a, q := sendFixture(t, kind)
			candidate := q.Target
			if kind == "user" {
				candidate.Containers = nil
			}
			resolution, err := ResolveSendTarget(context.Background(), platform.TargetLocator{Candidate: &candidate}, a, f)
			if err != nil || !resolution.Complete || resolution.Target == nil || resolution.Target.Object != q.Target.Object || !resolution.ObjectVerified {
				t.Fatal(resolution, err)
			}
			calls := 0
			cap := SendActions(f, func(ctx context.Context, target platform.TargetIdentity, body, key string) (string, error) {
				calls++
				if target.Object != q.Target.Object || body != "精确正文\n\"test\" @./literal" || key != q.IdempotencyKey {
					t.Fatal(target, body, key)
				}
				return "om_receipt", nil
			}, "")[platform.OperationMessageSend]
			result, err := cap.Execute(context.Background(), q, a)
			if err != nil || result.Status != "succeeded" || calls != 1 || result.Account != a.Account || cap.Spec.Retry.MaxAttempts != 1 || cap.Spec.Retry.RetryUnknown || !cap.Spec.DirectRoomInstructionOnly {
				t.Fatal(result, err, calls)
			}
		})
	}
	f, a, _ := sendFixture(t, "user")
	r, err := ResolveSendTarget(context.Background(), platform.TargetLocator{Query: &platform.TargetQuery{ObjectType: "user", Text: "同名用户"}}, a, f)
	if err != nil || r.Target != nil || r.Complete || len(r.Candidates) != 2 || !r.CandidatesIncomplete {
		t.Fatal("search automatically chose recipient", r, err)
	}
}
func TestSendRejectsBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"disabled", "body", "extra", "oversize", "utf8", "operation", "provider", "instance", "app", "tenant", "self", "peer", "p2p_as_group", "group_as_user", "thread", "message", "containers", "membership", "key"} {
		t.Run(mode, func(t *testing.T) {
			f, a, q := sendFixture(t, "user")
			disabled := ""
			switch mode {
			case "disabled":
				disabled = SendDisabledReason
			case "body":
				q.Input = json.RawMessage(`{"body":" "}`)
			case "extra":
				q.Input = json.RawMessage(`{"body":"x","receive_id_type":"chat_id"}`)
			case "oversize":
				q.Input, _ = json.Marshal(SendInput{Body: strings.Repeat("中", 1366)})
			case "utf8":
				q.Input = append([]byte(`{"body":"`), append([]byte{255}, []byte(`"}`)...)...)
			case "operation":
				q.Operation = platform.OperationMessageReply
			case "provider":
				q.Target.Provider = "gitlab"
			case "instance":
				q.Target.Instance = "https://other.invalid"
			case "app":
				q.Target.Object.ID.Namespace = "lark:open_id:other_app"
			case "tenant":
				q.Target.Tenant.Value = "other"
			case "self":
				q.Target.Object.ID = a.Account.ID
			case "peer":
				q.Target.Containers[0].ID.Value = "oc_other"
			case "p2p_as_group":
				q.Target.Object = platform.NativeIdentity{Type: "chat", ID: q.Target.Containers[0].ID}
				q.Target.Containers = nil
				f.group = f.peer
			case "group_as_user":
				f.peer.Mode = "group"
			case "thread":
				q.Target.Discussion = platform.ExternalID{Namespace: "lark:thread_id:cli_fixture", Value: "omt_thread"}
			case "message":
				q.Target.Message = platform.ExternalID{Namespace: nativeNamespace(a.Account, "message_id"), Value: "om_original"}
			case "containers":
				q.Target.Containers = nil
			case "membership":
				f.denied = true
			case "key":
				q.IdempotencyKey = "arbitrary"
			}
			calls := 0
			result, err := SendActions(f, func(context.Context, platform.TargetIdentity, string, string) (string, error) {
				calls++
				return "om_receipt", nil
			}, disabled)[platform.OperationMessageSend].Execute(context.Background(), q, a)
			if err != nil || result.Status != "not_applied" || calls != 0 {
				t.Fatal(mode, result, err, calls)
			}
		})
	}
}
func TestSendUncertainReceiptNeverRetries(t *testing.T) {
	for _, mode := range []string{"timeout", "not_sent", "bad_id", "missing", "body", "sender", "sender_type", "tenant", "chat", "parent", "root", "thread", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			f, a, q := sendFixture(t, "user")
			m := f.messages["om_receipt"]
			switch mode {
			case "body":
				m.Body.Content = `{"text":"different"}`
			case "sender":
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
			if mode == "missing" {
				delete(f.messages, m.ID)
			}
			calls := 0
			result, err := SendActions(f, func(context.Context, platform.TargetIdentity, string, string) (string, error) {
				calls++
				switch mode {
				case "timeout":
					return "", context.DeadlineExceeded
				case "not_sent":
					return "", ErrSendNotSent
				case "bad_id":
					return "bad", nil
				}
				return m.ID, nil
			}, "")[platform.OperationMessageSend].Execute(context.Background(), q, a)
			want := "unknown"
			if mode == "not_sent" {
				want = "not_applied"
			}
			if err != nil || result.Status != want || calls != 1 || result.RetryClass != "never" {
				t.Fatal(result, err, calls)
			}
		})
	}
}
