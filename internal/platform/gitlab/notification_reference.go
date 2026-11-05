package gitlab

import (
	"strconv"

	"github.com/go-aie/teamx/internal/platform"
)

func restID(id int64, namespace string) platform.ExternalID {
	if id <= 0 {
		return platform.ExternalID{}
	}
	return platform.ExternalID{Namespace: "gitlab:rest:" + namespace, Value: strconv.FormatInt(id, 10)}
}

func objectReference(kind string, id, iid int64) platform.ObjectReference {
	return platform.ObjectReference{Type: kind, ID: restID(id, "id"), IID: restID(iid, "iid")}
}

func eventReference(s EventsState, e Event) platform.NotificationReference {
	target := objectReference(e.TargetType, e.TargetID, e.TargetIID)
	object := target
	if e.Kind == "note" {
		// The top-level target can be the Note itself, never assume it is an MR.
		object = objectReference(e.NoteableType, e.NoteableID, e.NoteableIID)
	}
	return platform.NotificationReference{
		Version: 1, RecipientID: restID(s.UserID, "id"), Scope: "project",
		ProjectID: restID(s.ProjectID, "id"), Target: target, Object: object,
		NoteID: restID(e.NoteID, "id"), Branch: e.Branch, Ref: e.Ref,
		RefType: e.RefType, SHA: e.SHA, BeforeSHA: e.BeforeSHA,
	}
}

func todoReference(s TodosState, t Todo) platform.NotificationReference {
	object := objectReference(t.TargetType, t.TargetID, t.TargetIID)
	// A URL fragment is not verified comment/discussion evidence.
	return platform.NotificationReference{
		Version: 1, RecipientID: restID(s.Binding.UserID, "id"), Scope: "account",
		ProjectID: restID(t.ProjectID, "id"), Target: object, Object: object,
	}
}
