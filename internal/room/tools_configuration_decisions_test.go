package room

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestLarkToolGrantPlan(t *testing.T) {
	caps := lark.AcceptedActionCapabilities(nil, nil, nil)
	defaults := defaultLarkActionGrants(caps)
	reply, send, self := defaults[0], defaults[1], defaults[2]
	auth := func(grants ...tool.ActionGrant) *tool.ActionAuthorization {
		return &tool.ActionAuthorization{Version: 1, Revision: 7, Grants: grants}
	}
	for _, tc := range []struct {
		name              string
		actions           []string
		explicit, old     *tool.ActionAuthorization
		requested, grants []tool.ActionGrant
		reduction, bad    bool
	}{
		{name: "defaults", requested: defaults, grants: defaults},
		{name: "explicit actions", actions: []string{"message.send", "message.reply", "message.reply"}, requested: []tool.ActionGrant{reply, send}, grants: []tool.ActionGrant{reply, send}},
		{name: "historical read only", old: nil, actions: []string{"message.reply"}, requested: []tool.ActionGrant{reply}, grants: []tool.ActionGrant{reply}},
		{name: "preserve broader intent", actions: []string{"message.reply"}, old: auth(reply, send), requested: []tool.ActionGrant{reply}, grants: []tool.ActionGrant{reply, send}},
		{name: "default expansion", old: auth(reply), requested: defaults, grants: defaults},
		{name: "canonical existing grants", old: auth(self, send, reply), requested: defaults, grants: defaults},
		{name: "explicit reduction", explicit: auth(reply), old: auth(reply, send), requested: []tool.ActionGrant{reply}, grants: []tool.ActionGrant{reply}, reduction: true},
		{name: "replacement is not reduction", explicit: auth(self), old: auth(reply, send), requested: []tool.ActionGrant{self}, grants: []tool.ActionGrant{self}},
		{name: "same grants", explicit: auth(send, reply), old: auth(reply, send), requested: []tool.ActionGrant{reply, send}, grants: []tool.ActionGrant{reply, send}},
		{name: "unknown action", actions: []string{"future.write"}, bad: true},
		{name: "invalid target kind", explicit: auth(tool.ActionGrant{Operation: reply.Operation, TargetKind: "object", ObjectType: reply.ObjectType}), bad: true},
		{name: "unsupported object", explicit: auth(tool.ActionGrant{Operation: send.Operation, TargetKind: send.TargetKind, ObjectType: "user"}), bad: true},
		{name: "empty explicit", explicit: auth(), requested: []tool.ActionGrant{}, grants: []tool.ActionGrant{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := tool.Binding{ActionAuthorization: tc.explicit}
			var old *tool.Binding
			if tc.old != nil {
				old = &tool.Binding{ActionAuthorization: tc.old}
			}
			before, _ := json.Marshal([]any{binding, old, tc.actions})
			requested, grants, err := larkToolGrantPlan(caps, binding, tc.actions, old)
			if tc.bad {
				if !errors.Is(err, ErrRequest) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || !slices.Equal(requested, tc.requested) || !slices.Equal(grants, tc.grants) || strictLarkGrantReduction(tc.explicit != nil, old, grants) != tc.reduction {
				t.Fatal(requested, grants, err)
			}
			if len(grants) > 0 {
				grants[0].Operation = "mutated"
			}
			if len(requested) > 0 {
				requested[0].Operation = "mutated"
			}
			after, _ := json.Marshal([]any{binding, old, tc.actions})
			if string(before) != string(after) {
				t.Fatal("grant plan mutated input")
			}
		})
	}
}

func TestPlatformMemberIntentNormalizationRetainsObservation(t *testing.T) {
	revision := uint64(0)
	original := platformMemberInput{PlatformID: "platform", PlatformVersion: 2, ExpectedBindingRevision: &revision, Actions: []string{"message.send", "message.reply", "message.reply"}}
	got := normalizePlatformMemberInput(&original)
	if !slices.Equal(got.Actions, []string{"message.reply", "message.send"}) || !slices.Equal(original.Actions, []string{"message.send", "message.reply", "message.reply"}) || got.ExpectedBindingRevision == nil || *got.ExpectedBindingRevision != 0 {
		t.Fatal(got, original)
	}
	for _, actions := range [][]string{nil, {}} {
		in := original
		in.Actions = actions
		normalized := normalizePlatformMemberInput(&in)
		raw, _ := json.Marshal(normalized)
		var reloaded platformMemberInput
		if err := json.Unmarshal(raw, &reloaded); err != nil || !reflect.DeepEqual(*normalized, *normalizePlatformMemberInput(&reloaded)) {
			t.Fatal("omitted actions changed persisted intent", string(raw), err)
		}
		if normalized.Actions != nil {
			t.Fatal("default intent not canonical")
		}
	}
	if normalizePlatformMemberInput(nil) != nil {
		t.Fatal("nil intent changed")
	}
	unobserved := *got
	unobserved.ExpectedBindingRevision = nil
	if reflect.DeepEqual(*got, unobserved) {
		t.Fatal("omission erased explicit observation")
	}
}

func TestToolBindingRevisionMatchesExactTarget(t *testing.T) {
	b := tool.Binding{AgentID: "agent", MembershipID: "member", Tool: "lark", PlatformID: "account"}
	f := toolBindingsFile{Revision: 99, TargetRevisions: map[string]uint64{toolBindingKey(b): 2}}
	for _, tc := range []struct {
		name     string
		binding  tool.Binding
		expected *uint64
		conflict bool
	}{
		{"current", b, new(uint64(2)), false},
		{"omitted", b, nil, false},
		{"stale", b, new(uint64(1)), true},
		{"ABA tombstone", b, new(uint64(0)), true},
		{"other member", tool.Binding{AgentID: b.AgentID, MembershipID: "other", Tool: b.Tool, PlatformID: b.PlatformID}, new(uint64(0)), false},
		{"other account", tool.Binding{AgentID: b.AgentID, MembershipID: b.MembershipID, Tool: b.Tool, PlatformID: "other"}, new(uint64(0)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkBindingRevision(f, tc.binding, tc.expected); errors.Is(err, ErrConflict) != tc.conflict {
				t.Fatal(err)
			}
		})
	}
}
