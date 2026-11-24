package gitlab

import (
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// NotificationCapabilities keeps own-account progress and receipt matching in
// the adapter. It also covers stored content written before context_only existed.
func NotificationCapabilities() notification.Capabilities {
	return notification.Capabilities{
		Input: InputCapability(),
		Project: func(item notification.Item) notification.View {
			v := notification.Project(item)
			v.ContextOnly = v.ContextOnly || v.Sender != nil && v.Sender.IsReceiver &&
				positiveREST(v.Receiver.ID, "id") && v.Sender.ID == v.Receiver.ID
			object := item.Source.Reference.Object
			if v.Context != nil && actionResource(object.Type) != "" && positiveREST(object.ID, "id") {
				v.Context.Type, v.Context.ID = object.Type, object.ID
			}
			return v
		},
		MatchAction: func(v notification.View, q platform.TargetActionRequest, a platform.AccessContext) bool {
			// The ledger independently matches the returned, globally unique note
			// ID. A URL fragment or matching text cannot supply missing object evidence.
			t := q.Target
			return a.Valid() && t.Valid() && v.Provider == "gitlab" && v.Receiver == a.Account &&
				t.IdentityScope == a.Account.IdentityScope && len(t.Containers) == 1 &&
				t.Containers[0].Type == "Project" && positiveREST(t.Containers[0].ID, "id") &&
				restID(v.ProjectID, "id") == t.Containers[0].ID && actionResource(t.Object.Type) != "" &&
				v.Context != nil && v.Context.Type == t.Object.Type && v.Context.ID == t.Object.ID &&
				positiveREST(v.MessageID, "id") && (!v.ThreadID.Valid() || v.ThreadID == t.Discussion) &&
				(q.Operation == platform.OperationObjectComment && !t.Discussion.Valid() ||
					q.Operation == platform.OperationDiscussionReply && t.Discussion.Valid())
		},
	}
}
