package lark

import (
	"context"
	"errors"
	"github.com/go-aie/teamx/internal/platform"
	"testing"
)

type selfTestReader struct {
	*sendTestReader
	self Chat
}

func (f *selfTestReader) SelfChat(context.Context, platform.AccountIdentity) (Chat, error) {
	return f.self, nil
}

func TestSelfSendExactIdentityAndReceipt(t *testing.T) {
	for _, mode := range []string{"ok", "peer", "group", "namespace", "tenant", "container", "incomplete", "body", "author", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			base, a, q := sendFixture(t, "user")
			q.Operation = platform.OperationMessageSendSelf
			q.Target.Object.ID = a.Account.ID
			chat := base.peer
			chat.Relation = ConversationRelation{Account: a.Account, ChatID: chat.ID, Kind: "self"}
			reader := &selfTestReader{base, chat}
			switch mode {
			case "peer":
				q.Target.Object.ID.Value = "ou_peer"
			case "group":
				q.Target.Object.Type = "chat"
				q.Target.Object.ID = platform.ExternalID{Namespace: nativeNamespace(a.Account, "chat_id"), Value: "oc_group"}
				q.Target.Containers = nil
			case "namespace":
				q.Target.Object.ID.Namespace = "lark:open_id:cli_other"
			case "tenant":
				q.Target.Tenant.Value = "other"
			case "container":
				q.Target.Containers[0].ID.Value = "oc_other"
			case "incomplete":
				reader.self.Relation = ConversationRelation{}
			case "body":
				m := base.messages["om_receipt"]
				m.Body.Content = `{"text":"different"}`
				base.messages[m.ID] = m
			case "author":
				m := base.messages["om_receipt"]
				m.Sender.ID = "ou_peer"
				base.messages[m.ID] = m
			}
			calls := 0
			cap := AcceptedActionCapabilities(reader, nil, func(context.Context, platform.TargetIdentity, string, string) (string, error) {
				calls++
				if mode == "unknown" {
					return "", context.DeadlineExceeded
				}
				return "om_receipt", nil
			}).Actions[platform.OperationMessageSendSelf]
			r, err := cap.Execute(t.Context(), q, a)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "ok" {
				if r.Status != "succeeded" || calls != 1 {
					t.Fatal(r, calls)
				}
				return
			}
			wantCalls := 0
			wantStatus := "not_applied"
			if mode == "body" || mode == "author" || mode == "unknown" {
				wantCalls = 1
				wantStatus = "unknown"
			}
			if calls != wantCalls || r.Status != wantStatus {
				t.Fatal(r, calls)
			}
		})
	}
	base, a, _ := sendFixture(t, "user")
	chat := base.peer
	chat.Relation = ConversationRelation{Account: a.Account, ChatID: chat.ID, Kind: "self"}
	reader := &selfTestReader{base, chat}
	caps := AcceptedActionCapabilities(reader, nil, nil)
	r, err := caps.Resolve(t.Context(), platform.TargetLocator{Self: true}, a)
	if err != nil || r.Target == nil || r.Target.Object.ID != a.Account.ID || len(r.Target.Containers) != 1 {
		t.Fatal(r, err)
	}
	candidate := *r.Target
	for _, locator := range []platform.TargetLocator{{Self: true, Candidate: &candidate}, {Self: true, TargetRef: "ref_bad"}, {Self: true, URL: "https://example.com"}, {Self: true, Query: &platform.TargetQuery{ObjectType: "user", Text: "name"}}} {
		r, err = caps.Resolve(t.Context(), locator, a)
		if err != nil || r.Target != nil || r.Complete {
			t.Fatal("self accepted alternate destination", r, err)
		}
	}
}

func TestSelfSendAccountCapacity(t *testing.T) {
	b := &requestBudget{capacity: 1}
	ctx := context.WithValue(t.Context(), accountContextKey{}, "account")
	release, err := b.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	queued, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, e := b.acquire(queued)
		if r != nil {
			r()
		}
		done <- e
	}()
	awaitQueue(t, b, "account", 1)
	if _, err = b.acquire(ctx); ErrorCode(err) != "send_capacity" {
		t.Fatal(err)
	}
	other := context.WithValue(t.Context(), accountContextKey{}, "other-account")
	r, err := b.acquire(other)
	if err != nil {
		t.Fatal(err)
	}
	r()
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal("waiting send did not cancel", e)
	}
}
