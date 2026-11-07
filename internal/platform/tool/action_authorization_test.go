package tool

import (
	"encoding/json"
	"testing"
)

func TestOldBindingHasNoNewActionGrant(t *testing.T) {
	var b Binding
	if err := json.Unmarshal([]byte(`{"agent_id":"a","membership_id":"m","tool":"lark"}`), &b); err != nil {
		t.Fatal(err)
	}
	if b.ActionAuthorization != nil || !b.ActionAuthorization.Valid() {
		t.Fatal(b)
	}
	a := &ActionAuthorization{Version: 1, Revision: 1, Grants: []ActionGrant{{Operation: "message.reply", TargetKind: "message", ObjectType: "message"}}}
	if !a.Valid() {
		t.Fatal(a)
	}
	a.Grants = append(a.Grants, a.Grants[0])
	if a.Valid() {
		t.Fatal("duplicate grant accepted")
	}
	a.Version = 2
	if a.Valid() {
		t.Fatal("unknown version accepted")
	}
}
