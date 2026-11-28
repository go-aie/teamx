package room

import (
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/scion"
)

func TestScionMemberInputBoundaries(t *testing.T) {
	on := true
	repo := "Owner/repo"
	types := []string{"issue"}
	interval := 60
	valid := platformMemberInput{PlatformID: "dddddddddddddddddddddddddddddddd", PlatformVersion: 1, Enabled: &on, Scion: &platformMemberScionPatch{Repository: &repo, ObjectTypes: &types, IntervalSeconds: &interval}}
	if !validPlatformMemberInput("notifications", valid) || !validPlatformMemberProviderInput("scion", "notifications", valid) || valid.pureNotificationToggle() {
		t.Fatal(valid)
	}
	if validPlatformMemberInput("tool", valid) || validPlatformMemberProviderInput("lark", "notifications", valid) || validPlatformMemberProviderInput("gitlab", "notifications", valid) {
		t.Fatal("cross-provider fields accepted")
	}
	for _, raw := range []platformMemberScionPatch{{}, {ObjectTypes: &[]string{}}, {ObjectTypes: &[]string{"review"}}, {IntervalSeconds: new(int)}, {BackfillSeconds: new(int(-1))}, {BackfillStart: new(time.Time)}} {
		in := valid
		in.Scion = &raw
		if validPlatformMemberInput("notifications", in) {
			t.Fatal("invalid Scion patch", raw)
		}
	}
	for _, p := range []platformMemberInput{{PlatformID: valid.PlatformID, PlatformVersion: 1, Enabled: &on, Actions: []string{"message.send"}}, {PlatformID: valid.PlatformID, PlatformVersion: 1, Enabled: &on, ProjectID: 1, ProjectPath: "Owner/repo"}, {PlatformID: valid.PlatformID, PlatformVersion: 1, Enabled: &on, AddChatIDs: []string{"oc_group"}}} {
		if validPlatformMemberProviderInput("scion", "notifications", p) || validPlatformMemberProviderInput("scion", "tool", p) {
			t.Fatal("Scion accepted foreign patch", p)
		}
	}
}

func TestScionSelectionOnlyExistingAccounts(t *testing.T) {
	if !validSelectionInput(platformSelectionInput{Provider: "scion", Kind: "account"}) || !validSelectionInput(platformSelectionInput{Provider: "scion", Kind: "account", Origin: "https://scion.example"}) {
		t.Fatal("existing account choice rejected")
	}
	for _, in := range []platformSelectionInput{{Provider: "scion", Kind: "groups"}, {Provider: "scion", Kind: "projects"}, {Provider: "scion", Kind: "account", Origin: "https://scion.example/path"}, {Provider: "scion", Kind: "account", Query: "guess"}} {
		if validSelectionInput(in) {
			t.Fatal(in)
		}
	}
}

func TestScionRepositorySelectionDraftBoundaries(t *testing.T) {
	in := platformSelectionInput{Provider: "scion", Kind: "repositories", PlatformID: "dddddddddddddddddddddddddddddddd", PlatformVersion: 1, Query: "Owner/"}
	draft := platformSelectionDraft{Repository: &scion.RepositoryEvidence{Name: "Owner/repo", Created: 100}}
	if !validSelectionInput(in) || !validSelectionDraft(in, draft) || !validSelectionDraft(in, platformSelectionDraft{}) {
		t.Fatal("repository selection rejected")
	}
	for _, other := range []platformSelectionInput{{Provider: "scion", Kind: "account"}, {Provider: "gitlab", Kind: "projects"}, {Kind: "groups"}} {
		if validSelectionDraft(other, draft) {
			t.Fatal("cross-provider repository accepted", other)
		}
	}
	for _, bad := range []platformSelectionDraft{{Repository: &scion.RepositoryEvidence{Name: "Owner/repo"}}, {Repository: draft.Repository, ProjectID: 1}, {Repository: draft.Repository, ChatIDs: []string{"oc_group"}}, {Repository: draft.Repository, Name: "unexpected"}} {
		if validSelectionDraft(in, bad) {
			t.Fatal("foreign or incomplete repository fields", bad)
		}
	}
}
