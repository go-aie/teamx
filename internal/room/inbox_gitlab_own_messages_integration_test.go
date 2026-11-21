//go:build integration || e2e

package room

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func TestOwnGitlabOriginUsesExactDurableReceipt(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		t.Run(fmt.Sprint(discussion), func(t *testing.T) {
			f := gitlabImageActionFixture(t, discussion)
			var receipt agentGitlabReceipt
			if err := f.executeCLI(t, &receipt); err != nil || receipt.Status != "succeeded" {
				t.Fatal(receipt, err)
			}
			item := gitlab.EventsMessage(gitlab.EventsState{Binding: platform.ProjectBinding{URL: f.connection.URL, Project: "group/project"}, ProjectID: 123, UserID: 101}, gitlab.EventsInboxEntry{Event: gitlab.Event{
				Kind: "note", Author: gitlab.EventAuthor{ID: 101}, NoteID: 99, NoteableType: "MergeRequest", NoteableID: 9001, NoteableIID: 42,
			}})
			e := InboxEntry{Version: 1, MemberIdentity: f.id, Seq: 3, Item: item, ReceivedAt: time.Now().UTC()}
			store := f.svc.store
			lookup := func() inboxMessageOrigin {
				t.Helper()
				store.mu.Lock()
				origins, err := store.inboxMessageOriginsLocked([]InboxEntry{e})
				store.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				return origins[inboxEntryPosition(e)]
			}
			if o := lookup(); o.Kind != "agent" || o.ActionID != f.action.ID || !inboxContextOnly(e) {
				t.Fatal("exact GitLab receipt did not explain own-account echo", o)
			}
			for _, mutate := range []func(*InboxEntry){
				func(e *InboxEntry) { e.Item.Source.Reference.NoteID.Value = "100" },
				func(e *InboxEntry) { e.Item.Source.ProjectID++ },
				func(e *InboxEntry) { e.Item.Source.Reference.Object.ID.Value = "9002" },
				func(e *InboxEntry) { e.Item.Source.Instance = "https://other.example.com" },
				func(e *InboxEntry) { e.Item.Source.Reference.RecipientID.Value = "102" },
			} {
				old := e
				mutate(&e)
				if o := lookup(); o.Kind == "agent" {
					t.Fatal("manual text or different native scope was misattributed", o)
				}
				e = old
			}
			dir := store.dir
			if err := f.m.close(); err != nil {
				t.Fatal(err)
			}
			var err error
			store, err = OpenStore(dir, f.id.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if o := lookup(); o.Kind != "agent" || o.ActionID != f.action.ID {
				t.Fatal("durable GitLab provenance lost on reopen", o)
			}
		})
	}
}
