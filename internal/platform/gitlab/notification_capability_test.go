package gitlab

import (
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestOwnGitlabNotificationScope(t *testing.T) {
	q, a := actionFixture("https://gitlab.example.com", false)
	state := EventsState{Binding: platform.ProjectBinding{URL: a.Account.Instance, Project: "g/p"}, ProjectID: 123, UserID: 13}
	event := Event{Kind: "note", Author: EventAuthor{ID: 13}, NoteID: 99, NoteableType: "MergeRequest", NoteableID: 9001, NoteableIID: 42}
	caps := NotificationCapabilities()
	item := EventsMessage(state, EventsInboxEntry{Event: event})
	v := caps.Project(item)
	if !v.ContextOnly || !caps.MatchAction(v, q, a) || item.Content.ContextOnly {
		t.Fatal("native own message was not projected as context without rewriting stored evidence", v)
	}
	for _, mutate := range []func(*notification.View){
		func(v *notification.View) { v.Receiver.Instance = "https://other.example.com" },
		func(v *notification.View) { v.Receiver.ID.Value = "14" },
		func(v *notification.View) { v.ProjectID++ },
		func(v *notification.View) { v.Context.ID.Value = "9002" },
		func(v *notification.View) { v.Context.Type = "Issue" },
		func(v *notification.View) { v.MessageID = platform.ExternalID{} },
		func(v *notification.View) {
			v.ThreadID = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "other"}
		},
	} {
		v = caps.Project(item)
		mutate(&v)
		if caps.MatchAction(v, q, a) {
			t.Fatal("receipt matched a different or incomplete native scope", v)
		}
	}
	for _, mutate := range []func(*notification.Item){
		func(i *notification.Item) { i.Content.Sender.ID.Value = "14" },
		func(i *notification.Item) { i.Content.Sender.ID.Namespace = "other:id" },
		func(i *notification.Item) { i.Content.Sender.IsReceiver = false },
		func(i *notification.Item) { i.Source.Reference.RecipientID = platform.ExternalID{} },
		func(i *notification.Item) { i.Content = nil },
	} {
		item = EventsMessage(state, EventsInboxEntry{Event: event})
		mutate(&item)
		if caps.Project(item).ContextOnly {
			t.Fatal("missing own-account evidence was guessed")
		}
	}
	// Todos use the same account comparison; identical text from another user
	// remains an incoming notification.
	for _, actor := range []int64{13, 14} {
		item := TodosMessage(TodosState{Binding: TodosBinding{URL: a.Account.Instance, UserID: 13}}, TodosInboxEntry{Todo: Todo{Actor: &TodoActor{ID: actor}}})
		if caps.Project(item).ContextOnly != (actor == 13) {
			t.Fatal("todo actor/recipient identity lost")
		}
	}
}

func TestOwnIssueCommentMatchesManagedReceipt(t *testing.T) {
	q, a := actionFixture("https://gitlab.example.com", true)
	q.Target.Object.Type, q.Aliases[0].Object.Type = "Issue", "Issue"
	q.Target.Message, q.Aliases[0].Message = restID(98, "id"), restID(98, "id")
	state := EventsState{Binding: platform.ProjectBinding{URL: a.Account.Instance, Project: "g/p"}, ProjectID: 123, UserID: 13}
	event := Event{Kind: "note", Author: EventAuthor{ID: 13}, NoteID: 99, NoteableType: "Issue", NoteableID: 9001, NoteableIID: 42}
	caps := NotificationCapabilities()
	v := caps.Project(EventsMessage(state, EventsInboxEntry{Event: event}))
	if !v.ContextOnly || !caps.MatchAction(v, q, a) {
		t.Fatal("issue reply was not matched to its own account/object", v)
	}
	q.Target.Object.Type = "MergeRequest"
	if caps.MatchAction(v, q, a) {
		t.Fatal("issue and MR identities were confused")
	}
}
