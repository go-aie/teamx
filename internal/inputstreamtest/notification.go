package inputstreamtest

import (
	"encoding/json"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// NativeID preserves namespace boundaries without delimiter collisions.
func NativeID(id platform.ExternalID) string {
	if !id.Valid() {
		return ""
	}
	b, _ := json.Marshal(id)
	return string(b)
}
func StoredInputGroup(item notification.Item) Group {
	s := item.Source
	r := s.Reference
	return Group{Version: GroupingVersion, Kind: "notification", Provider: s.Provider, Channel: s.Channel, Instance: s.Instance,
		Tenant: NativeID(r.AccountTenant), Account: NativeID(r.RecipientID), Subscription: s.SubscriptionID,
		Precision: "source", Missing: "native_context_missing", Context: []string{"subscription", s.SubscriptionID},
		Facts: Facts{Label: s.Object, SourceLabel: s.Provider, Project: s.Project, Summary: item.Text}}
}

type InputCapability struct {
	Descriptor       Descriptor
	GroupStoredInput func(notification.Item) (Group, error)
}
