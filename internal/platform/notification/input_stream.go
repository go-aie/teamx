package notification

import (
	"encoding/json"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
)

func NativeID(id platform.ExternalID) string {
	if !id.Valid() {
		return ""
	}
	b, _ := json.Marshal(id)
	return string(b)
}

// StoredInputGroup is only an ingress fallback. No native field union is stored
// or queried by the common input layer.
func StoredInputGroup(item Item) inputstream.Group {
	s := item.Source
	r := s.Reference
	return inputstream.Group{Version: 1, Instance: s.Instance, Tenant: NativeID(r.AccountTenant), Account: NativeID(r.RecipientID), Subscription: s.SubscriptionID, Precision: "source", Missing: "native_context_missing", Context: []string{"subscription", s.SubscriptionID}}
}
