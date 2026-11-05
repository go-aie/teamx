package gitlab

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestEventNotificationPreservesNativeReferences(t *testing.T) {
	s := EventsState{ProjectID: 123, UserID: 101}
	s.Binding.URL, s.Binding.Project = "https://gitlab.example.com", "group/project"
	e := Event{ID: 1, Kind: "note", TargetType: "Note", TargetID: 77,
		NoteID: 77, NoteableType: "MergeRequest", NoteableID: 9001, NoteableIID: 42,
		Branch: "main", Ref: "refs/heads/main", RefType: "branch", SHA: "after", BeforeSHA: "before"}
	item := EventsMessage(s, EventsInboxEntry{Event: e})
	r := item.Source.Reference
	if !r.Valid() || r.Version != 1 || r.Target.Type != "Note" || r.Target.ID != restID(77, "id") ||
		r.Object.Type != "MergeRequest" || r.Object.ID != restID(9001, "id") || r.Object.IID != restID(42, "iid") ||
		r.NoteID != restID(77, "id") || r.ProjectID != restID(123, "id") || r.RecipientID != restID(101, "id") ||
		r.Branch != e.Branch || r.Ref != e.Ref || r.RefType != e.RefType || r.SHA != e.SHA || r.BeforeSHA != e.BeforeSHA {
		t.Fatal("native source evidence lost", r)
	}
	b, err := json.Marshal(item.Source)
	if err != nil {
		t.Fatal(err)
	}
	var source platform.SourceRef
	if err := json.Unmarshal(b, &source); err != nil || source != item.Source {
		t.Fatal("reference changed on persistence", source, err)
	}
	e.NoteableType, e.NoteableID, e.NoteableIID = "", 0, 0
	missing := EventsMessage(s, EventsInboxEntry{Event: e}).Source.Reference
	if missing.Object != (platform.ObjectReference{}) || missing.Target.ID != restID(77, "id") {
		t.Fatal("comment target guessed as business object", missing)
	}
	e.Kind, e.TargetType, e.TargetID, e.TargetIID = "merge_request", "MergeRequest", 9001, 42
	r = EventsMessage(s, EventsInboxEntry{Event: e}).Source.Reference
	if r.Object != r.Target || r.Object.ID == r.Object.IID {
		t.Fatal("global ID and IID collapsed", r)
	}
}

func TestTodoNotificationPreservesAccountAndObjectEvidence(t *testing.T) {
	s := TodosState{Binding: TodosBinding{URL: "https://gitlab.example.com", UserID: 101, Username: "reader"}}
	todo := Todo{ID: 9, RecipientID: 101, ProjectID: 123, Project: "group/project", TargetType: "MergeRequest", TargetID: 9001, TargetIID: 42,
		URL: "https://gitlab.example.com/group/project/-/merge_requests/42#note_77"}
	item := TodosMessage(s, TodosInboxEntry{Todo: todo})
	r := item.Source.Reference
	if !r.Valid() || r.Scope != "account" || r.RecipientID != restID(101, "id") || r.ProjectID != restID(123, "id") ||
		r.Object != objectReference("MergeRequest", 9001, 42) || r.Target != r.Object ||
		r.NoteID != (platform.ExternalID{}) || r.DiscussionID != (platform.ExternalID{}) {
		t.Fatal("todo guessed or lost source evidence", r)
	}
	todo.Project = "renamed/project"
	if renamed := TodosMessage(s, TodosInboxEntry{Todo: todo}).Source.Reference; renamed != r {
		t.Fatal("project display path changed native identity", renamed, r)
	}
	todo.TargetID = 0
	if missing := TodosMessage(s, TodosInboxEntry{Todo: todo}).Source.Reference; missing.Object.ID != (platform.ExternalID{}) || missing.Object.IID != restID(42, "iid") {
		t.Fatal("IID guessed as global ID", missing)
	}
	// Opaque IDs retain both namespace and exact value at the generic boundary.
	r.NoteID = platform.ExternalID{Namespace: "gitlab:graphql:gid", Value: "gid://gitlab/Note/77"}
	b, _ := json.Marshal(r)
	var restored platform.NotificationReference
	if err := json.Unmarshal(b, &restored); err != nil || !reflect.DeepEqual(r, restored) {
		t.Fatal(restored, err)
	}
}
