package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestAgentPolicyFieldDiagnostics(t *testing.T) {
	base := agentPolicyChange{Type: "save", ID: "contacts", SourceSeq: 8, TurnNumber: 14, Basis: "明确长期指令", Body: &agentPolicyBody{Kind: "rule", Text: "先联系指定负责人", Scope: PolicyScope{Provider: "gitlab", Instance: "https://gitlab.example.com", Account: "alice", Project: "g/p"}}}
	cases := []struct {
		name, field string
		change      func(*agentPolicyChange)
	}{
		{"missing ID", "id", func(q *agentPolicyChange) { q.ID = "" }},
		{"empty rule scope", "body.scope.provider", func(q *agentPolicyChange) { q.Body.Scope = PolicyScope{} }},
		{"missing account", "body.scope.account", func(q *agentPolicyChange) { q.Body.Scope.Account = "" }},
		{"missing project", "body.scope.project", func(q *agentPolicyChange) { q.Body.Scope.Project = "" }},
		{"wildcard project", "body.scope.project", func(q *agentPolicyChange) { q.Body.Scope.Project = "*" }},
		{"preference with scope", "body.scope", func(q *agentPolicyChange) { q.Body.Kind = "preference" }},
		{"empty text", "body.text", func(q *agentPolicyChange) { q.Body.Text = "" }},
		{"missing source", "source_seq", func(q *agentPolicyChange) { q.SourceSeq = 0 }},
		{"missing turn", "turn_number", func(q *agentPolicyChange) { q.TurnNumber = 0 }},
		{"missing basis", "basis", func(q *agentPolicyChange) { q.Basis = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			body := *base.Body
			q.Body = &body
			tc.change(&q)
			b, _ := json.Marshal(q)
			err := runCLI(context.Background(), []string{"policy", "update", "--change", string(b)}, io.Discard, io.Discard, func(string) string { return "" })
			if !errors.Is(err, ErrRequest) || !strings.Contains(err.Error(), tc.field+":") {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentPolicyScopeFromCLIAndRecovery(t *testing.T) {
	h, svc, units, _ := conversationHarness(t, 1)
	cli := entryCLI(t, svc)
	source := postConversationReply(t, h, "rule", "以后这类问题先联系指定负责人", 0)
	u := units[0]
	q := agentPolicyChange{Type: "save", ID: "contacts", SourceSeq: source.Seq, TurnNumber: 1, Basis: "用户明确长期要求", Body: &agentPolicyBody{Kind: "rule", Text: "先联系指定负责人"}, ScopeFrom: &agentPolicyScopeFrom{ObjectRef: u.ObjectRef, UnitRef: u.Ref, Level: "project"}}
	var result agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", q), &result); err != nil {
		t.Fatal(err)
	}
	if result.Record.Version != 1 || len(result.Effective) != 1 {
		t.Fatal(result)
	}
	scope := result.Record.Scope
	if scope.Provider != "gitlab" || scope.Account != "" || !scope.AccountID.Valid() || scope.Project == "" || scope.ObjectType == "" || scope.NoContainers {
		t.Fatal(scope)
	}
	// The native scope must cover the exact verified target with no display labels.
	h.store.mu.Lock()
	target, err := h.store.loadTargetLocked(h.id, u.ObjectRef, u.TargetRef)
	h.store.mu.Unlock()
	if err != nil || scope.Project != target.Target.Containers[0].ID.Value || scope.ObjectType != target.Target.Object.Type {
		t.Fatal(target, scope, err)
	}
	inspection := platform.TargetActionInspection{Target: target.Target, Account: platform.AccountIdentity{IdentityScope: target.Target.IdentityScope, ID: scope.AccountID}}
	if !referenceRuleCovers(PolicyRevision{Scope: scope}, inspection) {
		t.Fatal("restored rule does not cover source target")
	}
	inspection.Account.ID.Value = "different"
	if referenceRuleCovers(PolicyRevision{Scope: scope}, inspection) {
		t.Fatal("restored rule covers another account")
	}
	// Explicit and referenced forms normalize to the same persisted request.
	explicit := q
	explicit.ScopeFrom = nil
	body := *q.Body
	body.Scope = scope
	explicit.Body = &body
	if err := cli(entryChange(t, "policy", "update", explicit), &result); err != nil || result.Record.Version != 1 {
		t.Fatal(result, err)
	}
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	q.TurnNumber = 2
	recovered, err := h.store.changeAgentPolicy(h.id, agentCallerIdentity(h.id), q)
	if err != nil || recovered.Record.Version != 1 || recovered.Revision != 1 || recovered.Record.Scope != scope {
		t.Fatal(recovered, err)
	}
}

func TestAgentPolicyInvalidSourceAndBranchesKeepState(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	cli := entryCLI(t, svc)
	q := agentPolicyChange{Type: "save", ID: "style", SourceSeq: 999, TurnNumber: 1, Basis: "真实来源", Body: &agentPolicyBody{Kind: "preference", Text: "简短中文"}}
	before, err := h.store.readPolicy(h.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli(entryChange(t, "policy", "update", q), nil); err == nil || !strings.Contains(err.Error(), "source_seq:") || strings.Contains(err.Error(), "snapshot identity mismatch") {
		t.Fatal(err)
	}
	if _, err := h.store.changeAgentPolicy(h.id, agentCallerIdentity(h.id), q); !errors.Is(err, ErrIdentity) {
		t.Fatal("source diagnostic changed the identity sentinel", err)
	}
	status, failure := diagnosticHTTP(t, svc, apiRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(h.id), ConversationPolicy: &q}, "/v1/policy/update")
	if status != 403 || failure.Code != "identity" || failure.Field != "source_seq" || strings.Contains(failure.Hint, "member binding") {
		t.Fatal(status, failure)
	}
	source := postConversationReply(t, h, "style", "以后用简短中文", 0)
	q.SourceSeq = source.Seq
	var result agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", q), &result); err != nil {
		t.Fatal(err)
	}
	// A real evidence/state mismatch must retain the specific branch explanation.
	q.Type = "suggest"
	q.Version = 1
	q.Revision = 1
	q.SourceSeq = postConversationReply(t, h, "habit", "这次也简短", 0).Seq
	if err := cli(entryChange(t, "policy", "update", q), nil); err == nil || !strings.Contains(err.Error(), "suggest can only create or amend a suggested record") {
		t.Fatal(err)
	}
	after, err := h.store.readPolicy(h.id)
	if err != nil || after.Revision != before.Revision+1 || len(after.History) != 1 {
		t.Fatal(after, err)
	}
}

func TestAgentPolicyScopeFromRejectsMixedAndIncompleteInputs(t *testing.T) {
	ref := referenceDigest("ref_", "object")
	unit := referenceDigest("unit_", "target")
	base := `"type":"save","id":"contacts","source_seq":8,"turn_number":1,"basis":"explicit",`
	for _, tail := range []string{
		`"body":{"kind":"rule","text":"rule","scope":{}},"scope_from":{"object_ref":"` + ref + `","unit_ref":"` + unit + `","level":"project"}`,
		`"body":{"kind":"rule","text":"rule"},"scope_from":null`,
		`"body":{"kind":"preference","text":"preference"},"scope_from":{"object_ref":"` + ref + `","unit_ref":"` + unit + `","level":"project"}`,
		`"body":{"kind":"rule","text":"rule"},"scope_from":{"object_ref":"` + ref + `","unit_ref":"` + unit + `"}`,
		`"body":{"kind":"rule","text":"rule"},"scope_from":{"object_ref":"` + ref + `","unit_ref":"` + unit + `","level":"instance"}`,
		`"body":{"kind":"rule","text":"rule"},"scope_from":{"object_ref":"` + ref + `","unit_ref":"` + unit + `","level":"project","typo":true}`,
		`"version":1,"proposal_seq":7,"body":null,"scope_from":{"object_ref":"` + ref + `","unit_ref":"` + unit + `","level":"project"}`,
	} {
		var q agentPolicyChange
		if err := json.Unmarshal([]byte("{"+base+tail+"}"), &q); !errors.Is(err, ErrRequest) {
			t.Fatal(tail, err)
		}
	}
}

func TestAgentPolicyScopeFromRejectsUnverifiedOrWrongLevel(t *testing.T) {
	h, _, units, _ := conversationHarness(t, 1)
	source := postConversationReply(t, h, "rule", "以后先联系指定负责人", 0)
	q := agentPolicyChange{Type: "save", ID: "contacts", SourceSeq: source.Seq, TurnNumber: 1, Basis: "explicit", Body: &agentPolicyBody{Kind: "rule", Text: "联系负责人"}, ScopeFrom: &agentPolicyScopeFrom{ObjectRef: units[0].ObjectRef, UnitRef: units[0].Ref, Level: "no_containers"}}
	before, _ := h.store.readPolicy(h.id)
	if _, err := h.store.changeAgentPolicy(h.id, agentCallerIdentity(h.id), q); err == nil || !strings.Contains(err.Error(), "scope_from.level") {
		t.Fatal(err)
	}
	q.ScopeFrom.Level = "project"
	q.ScopeFrom.ObjectRef = referenceDigest("ref_", "another member object")
	if _, err := h.store.changeAgentPolicy(h.id, agentCallerIdentity(h.id), q); err == nil {
		t.Fatal("missing/foreign object accepted")
	}
	after, _ := h.store.readPolicy(h.id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected change mutated policy")
	}
}

func TestAgentPolicyScopeFromPlatformBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, level, field string
		containers         []platform.NativeIdentity
		ambiguous          bool
	}{
		{name: "tenant without containers", level: "no_containers"},
		{name: "project on containerless target", level: "project", field: "scope_from.level"},
		{name: "unsupported hierarchy", level: "project", field: "scope_from.level", containers: []platform.NativeIdentity{{Type: "Workspace", ID: platform.ExternalID{Namespace: "workspace", Value: "blue"}}}},
		{name: "multiple parents", level: "project", field: "scope_from.level", containers: []platform.NativeIdentity{{Type: "Project", ID: platform.ExternalID{Namespace: "project", Value: "blue"}}, {Type: "Project", ID: platform.ExternalID{Namespace: "project", Value: "green"}}}},
		{name: "ambiguous account", level: "no_containers", field: "scope_from", ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			initReferenceTest(t, h)
			receiveReference(t, h, "one", contractSource("one", "conversation-blue"))
			count := uint64(1)
			if tc.ambiguous {
				receiveReference(t, h, "two", contractSource("two", "conversation-blue"))
				count = 2
			}
			inboxRunning(t, h, 1, count)
			svc := referenceTestService(h)
			resolve := func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
				a := contractAccess(id, platform.IdentityScope{Provider: source.Provider, Instance: source.Instance, Tenant: source.Tenant})
				if tc.ambiguous && source.ItemKey == "two" {
					a.Account.ID.Value = "another-account"
					a.Receiver = a.Account
				}
				target := platform.TargetIdentity{IdentityScope: a.Account.IdentityScope, Containers: tc.containers, Object: platform.NativeIdentity{Type: "Conversation", ID: source.Reference.Object.ID}}
				return platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: a.Account, ObjectVerified: true, Complete: true, CheckedAt: time.Now().UTC()}, a, nil
			}
			page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, resolve)
			if err != nil {
				t.Fatal(err)
			}
			object := page.Sources[0].ObjectRef
			source := postConversationReply(t, h, "rule", "以后按这个规则处理", 0)
			q := agentPolicyChange{Type: "save", ID: "contract-rule", SourceSeq: source.Seq, TurnNumber: 1, Basis: "explicit", Body: &agentPolicyBody{Kind: "rule", Text: "准确业务要求"}, ScopeFrom: &agentPolicyScopeFrom{ObjectRef: object, UnitRef: referenceDigest("unit_", object), Level: tc.level}}
			cli := entryCLI(t, svc)
			var result agentPolicyUpdateWireTest
			err = cli(entryChange(t, "policy", "update", q), &result)
			if tc.field != "" {
				if err == nil || !strings.Contains(err.Error(), tc.field+":") {
					t.Fatal(result, err)
				}
				p, e := h.store.readPolicy(h.id)
				if e != nil || p.Revision != 0 || len(p.History) != 0 {
					t.Fatal(p, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			scope := result.Record.Scope
			if !scope.NoContainers || scope.Project != "" || scope.Account != "" || scope.AccountID != (platform.ExternalID{Namespace: "contract:account", Value: "account-alice"}) || scope.Tenant != contractSource("one", "conversation-blue").Tenant || scope.ObjectType != "Conversation" {
				t.Fatal(scope)
			}
			target := platform.TargetIdentity{IdentityScope: platform.IdentityScope{Provider: scope.Provider, Instance: scope.Instance, Tenant: scope.Tenant}, Object: platform.NativeIdentity{Type: "Conversation", ID: contractSource("one", "conversation-blue").Reference.Object.ID}}
			inspect := platform.TargetActionInspection{Target: target, Account: contractAccess(h.id, target.IdentityScope).Account}
			if !referenceRuleCovers(PolicyRevision{Scope: scope}, inspect) {
				t.Fatal("tenant scope does not cover exact target")
			}
			inspect.Account.Tenant.Value = "other-tenant"
			if referenceRuleCovers(PolicyRevision{Scope: scope}, inspect) {
				t.Fatal("tenant scope leaked")
			}
		})
	}
}
