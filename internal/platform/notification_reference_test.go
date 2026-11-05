package platform

import "testing"

func TestNotificationReferenceNamespaces(t *testing.T) {
	rest := ExternalID{Namespace: "gitlab:rest:id", Value: "9001"}
	iid := ExternalID{Namespace: "gitlab:rest:iid", Value: "9001"}
	gid := ExternalID{Namespace: "gitlab:graphql:gid", Value: "gid://gitlab/Note/9001"}
	if rest == iid || rest == gid || iid == gid {
		t.Fatal("ID namespaces collapsed")
	}
	for _, r := range []NotificationReference{
		{}, {Version: 1}, {Version: 1, Object: ObjectReference{Type: "WorkItem", ID: gid}},
	} {
		if !r.Valid() {
			t.Fatal("valid partial evidence rejected", r)
		}
	}
	for _, r := range []NotificationReference{
		{Version: 2}, {Object: ObjectReference{Type: "Issue"}},
		{Version: 1, NoteID: ExternalID{Value: "77"}},
		{Version: 1, NoteID: ExternalID{Namespace: "gitlab:rest:id"}},
		{Version: 1, Object: ObjectReference{ID: rest}},
	} {
		if r.Valid() {
			t.Fatal("invalid reference accepted", r)
		}
	}
}
