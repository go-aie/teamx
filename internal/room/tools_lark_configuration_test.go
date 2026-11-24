package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// A real CLI process fixture accepts reads only. Even a mistaken write dispatch
// fails here; these tests cannot send a native message or start OAuth.
func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func larkToolConfigurationHarness(t *testing.T) *larkHarness {
	t.Helper()
	return larkConfigurationHarness(t, 0)
}

func larkToolConfigurationHarnessWithMembers(t *testing.T, count int) *larkHarness {
	t.Helper()
	return larkConfigurationHarness(t, count)
}

func writeJSONFixture(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func larkConfigurationChange(h *larkHarness) toolBindingChange {
	return toolBindingChange{ExpectedBindingRevision: new(uint64(0)), ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, PlatformVersion: h.c.Version, Enabled: true, Binding: tool.Binding{AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID, PlatformID: h.c.ID, Tool: "lark"}}
}
func larkMemberToolRequest(t *testing.T, h *larkHarness, revision uint64) platformMemberRequest {
	t.Helper()
	id, _ := newOpaqueID()
	post, err := h.svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ClientMessageID: id, Text: "为当前助理启用飞书工具"})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	return platformMemberRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID), OperationID: id, SourceSeq: post.Seq, Input: &platformMemberInput{PlatformID: h.c.ID, PlatformVersion: h.c.Version, ExpectedBindingRevision: &revision, Enabled: &enabled}}
}
func larkOnboardingToolRequest(t *testing.T, h *larkHarness) platformOnboardingRequest {
	t.Helper()
	id, _ := newOpaqueID()
	enabled := true
	return platformOnboardingRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, OperationID: id, platformOnboardingSpec: platformOnboardingSpec{MemberIdentity: MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}, PlatformID: h.c.ID, Provider: "lark", Origin: h.c.URL, SkipNotifications: true, Tool: &enabled}}
}
func configurationBindings(t *testing.T, h *larkHarness) toolBindingsFile {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	f, err := h.m.readToolBindingsLocked(h.room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func TestLarkToolConfigurationEntrypointsAgreeAndSelfProofIsRequired(t *testing.T) {
	parallelFixtureTest(t)
	for _, entry := range []string{"http", "member", "onboarding"} {
		t.Run(entry, func(t *testing.T) {
			h := larkToolConfigurationHarnessWithMembers(t, 1)
			var result *larkMemberCapabilityResult
			switch entry {
			case "http":
				r := managerHTTP(h.m, "/v1/tools/bind", larkConfigurationChange(h))
				if r.Code != 200 {
					t.Fatal(r.Code, r.Body.String())
				}
				var body struct {
					Capabilities *larkMemberCapabilityResult `json:"capabilities"`
				}
				if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				result = body.Capabilities
			case "member":
				req := larkMemberToolRequest(t, h, 0)
				req.Input.Actions = []string{}
				op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
				if err != nil || op.Status != "completed" || op.Schema != 3 {
					t.Fatal(op, err)
				}
				result = op.Capabilities
				req.Input.Actions = nil // omitted defaults must replay the persisted empty plan
				again, err := h.m.configurePlatformMember(t.Context(), "tool", req)
				if err != nil || !sameJSON(again, op) {
					t.Fatal("receipt replay changed", err)
				}
			case "onboarding":
				req := larkOnboardingToolRequest(t, h)
				h.m.mu.Lock()
				prepared, err := h.m.prepareOnboardingLocked(t.Context(), req)
				h.m.mu.Unlock()
				if err != nil || len(prepared.LarkActions) != 3 {
					t.Fatal(prepared, err)
				}
				op, err := h.m.applyOnboarding(t.Context(), req.OperationID, humanCaller.ID)
				if err != nil || op.Tool.Status != "applied" {
					t.Fatal(op.Tool, err)
				}
				result = op.Tool.Capabilities
				again, err := h.m.applyOnboarding(t.Context(), req.OperationID, humanCaller.ID)
				if err != nil || !sameJSON(again.Tool, op.Tool) {
					t.Fatal("onboarding replay changed", err)
				}
			}
			f := configurationBindings(t, h)
			if result == nil || len(f.Bindings) != 1 || f.Revision != 1 || f.Bindings[0].ActionAuthorization == nil || len(f.Bindings[0].ActionAuthorization.Grants) != 3 || len(f.Bindings[0].CapabilityEvidence) != 0 {
				t.Fatal("inconsistent defaults", f, result)
			}
			if c := memberCapability(t, *result, "message.send_self"); c.State == "ready" || c.Configured == nil || !*c.Configured {
				t.Fatal("unverified self reported executable", c)
			}
			// The actual execution guard rejects the granted self action without proof.
			b := f.Bindings[0]
			a, _, err := h.m.resolvePlatformAccountLocked(h.room.RoomIdentity, h.c.ID, "lark")
			if err != nil {
				t.Fatal(err)
			}
			access := larkReferenceAccess(MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: b.AgentID, MembershipID: b.MembershipID}, a, platform.SourceRef{})
			access.ActionAuthorizationVersion = b.ActionAuthorization.Revision
			target := platform.TargetIdentity{IdentityScope: a.Identity.IdentityScope, Object: platform.NativeIdentity{Type: "user", ID: a.Identity.ID}}
			if err = h.m.checkActionAuthorizationLocked(toolsRequest{ServiceInfo: h.svc.info, Caller: agentCaller(b.AgentID, b.MembershipID)}, access, platform.ActionSpec{Operation: platform.OperationMessageSendSelf, TargetKind: "object", BindingEvidence: "self_delivery"}, target); err == nil {
				t.Fatal("grant alone admitted self execution")
			}
		})
	}
}
func TestLarkToolConfigurationExplicitActionsAndPreservedIntent(t *testing.T) {
	parallelFixtureTest(t)
	h := larkToolConfigurationHarnessWithMembers(t, 1)
	// A historical read-only binding remains unchanged by status/list reads.
	change := larkConfigurationChange(h)
	h.m.mu.Lock()
	_, _, err := h.m.changeToolBindingLocked(t.Context(), change)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	before := configurationBindings(t, h)
	req := larkMemberToolRequest(t, h, 1)
	req.Input.Actions = []string{"message.reply"}
	op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Result.Status != "applied" || len(op.Authorization.Grants) != 1 || op.Authorization.Grants[0].Operation != "message.reply" {
		t.Fatal(op, err)
	}
	if before.Bindings[0].ActionAuthorization != nil {
		t.Fatal("historical binding mutated")
	}
	req = larkMemberToolRequest(t, h, 2)
	req.Input.Actions = []string{"message.send", "message.reply"}
	op, err = h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || len(op.Authorization.Grants) != 2 {
		t.Fatal(op, err)
	}
	req = larkMemberToolRequest(t, h, 3)
	req.Input.Actions = []string{"message.reply"}
	op, err = h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Result.Status != "preserved" || op.Result.Version != 3 || len(op.Authorization.Grants) != 2 {
		t.Fatal(op, err)
	}
}
func TestLarkToolConfigurationUserAndApplicationScopeAdmission(t *testing.T) {
	parallelFixtureTest(t)
	h := larkToolConfigurationHarnessWithMembers(t, 1)
	writeJSONFixture(t, filepath.Join(h.dir, "missing-scopes.json"), []string{"im:message:readonly"})
	req := larkMemberToolRequest(t, h, 0)
	op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Capabilities == nil {
		t.Fatal(op, err)
	}
	if op.Status != "not_applied" || op.Result.ErrorCode != "user_authorization_required" {
		t.Fatal(op)
	}
	if f := configurationBindings(t, h); f.Revision != 0 {
		t.Fatal("OAuth gap wrote binding", f)
	}
	if _, err = os.Stat(h.m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
		t.Fatal("OAuth gap reserved config operation", err)
	}
	r := managerHTTP(h.m, "/v1/tools/bind", larkConfigurationChange(h))
	if r.Code != 409 || !strings.Contains(r.Body.String(), "user_authorization_required") {
		t.Fatal(r.Code, r.Body.String())
	}
	// App gaps cannot be repaired by OAuth and are saved as partial intent.
	scopes := slices.DeleteFunc(lark.DefaultScopes(), func(s string) bool { return s == "im:message:readonly" })
	writeJSONFixture(t, filepath.Join(h.dir, "application-scopes.json"), scopes)
	op, err = h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Status != "completed" || op.Capabilities == nil || memberCapability(t, *op.Capabilities, "message.read").ReasonCode != "application_scopes_missing" {
		t.Fatal(op, err)
	}
}

func TestLarkToolConfigurationRechecksAfterRemotePreparation(t *testing.T) {
	parallelFixtureTest(t)
	for _, mutation := range []string{"disable", "credential", "generation", "member", "cancel"} {
		t.Run(mutation, func(t *testing.T) {
			h := larkToolConfigurationHarnessWithMembers(t, 1)
			change := larkConfigurationChange(h)
			// The callback itself acquires manager.mu: remote work must be outside it.
			h.m.bindingEvidencePreparers["lark"] = func(ctx context.Context, _ *toolBindingChange) error {
				h.m.mu.Lock()
				defer h.m.mu.Unlock()
				switch mutation {
				case "disable":
					off := change
					off.Enabled = false
					off.PlatformVersion = 0
					_, _, err := h.m.changeToolBindingLocked(ctx, off)
					if err != nil {
						t.Fatal(err)
					}
				case "credential":
					ticket, err := h.m.platforms.BeginVerification(h.c.ID, h.c.Version)
					if err != nil {
						t.Fatal(err)
					}
					_, err = h.m.platforms.CompleteVerification(ticket, h.c.Validation)
					if err != nil {
						t.Fatal(err)
					}
				case "generation":
					// A platform edit rotates its generation/version through the registry.
					profile := *h.c.CLIProfile
					profile.Profile = "replacement"
					plan, err := h.m.platforms.Plan(registry.Request{ID: h.c.ID, Version: h.c.Version, Name: h.c.Name, Provider: "lark", URL: h.c.URL, CLIProfile: &profile}, false)
					if err == nil {
						_, err = h.m.platforms.Commit(plan)
					}
					if err != nil {
						t.Fatal(err)
					}
				case "member":
					// Remove the exact live membership during native preparation.
					_, err := h.svc.room.Call[MemberInfo](ctx, RemoveMember{MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: change.Binding.AgentID, MembershipID: change.Binding.MembershipID}})
					if err != nil {
						t.Fatal(err)
					}
				case "cancel":
					for _, call := range h.m.platformCalls.Snapshot() {
						call.Cancel()
					}
				}
				return errors.New("native self check unavailable")
			}
			err := h.m.prepareBindingEvidence(t.Context(), &change)
			if err == nil {
				t.Fatal("stale preparation was accepted")
			}
			if f := configurationBindings(t, h); len(f.Bindings) > 0 {
				t.Fatal("stale preparation wrote binding")
			}
		})
	}
}

func TestLarkToolConfigurationNativeSelfEvidenceRefreshAndForgery(t *testing.T) {
	parallelFixtureTest(t)
	if runtime.GOOS != "darwin" {
		t.Skip("fixed-account self evidence requires macOS")
	}
	h := larkToolConfigurationHarnessWithMembers(t, 1)
	state := defaultLarkConfigurationState()
	state.Self = true
	writeJSONFixture(t, filepath.Join(h.dir, "native-fixture.json"), state)
	req := larkMemberToolRequest(t, h, 0)
	op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Status != "completed" {
		t.Fatal(op, err)
	}
	b := configurationBindings(t, h).Bindings[0]
	if b.Evidence("self_delivery") == nil {
		t.Fatal("native self proof was not saved")
	}
	for _, mutate := range []func(*platform.CapabilityEvidence){
		func(e *platform.CapabilityEvidence) { e.Operator = "other" },
		func(e *platform.CapabilityEvidence) { e.Account.ID.Value = "ou_other" },
		func(e *platform.CapabilityEvidence) { e.Target.Object.ID.Value = "ou_other" },
		func(e *platform.CapabilityEvidence) { e.CredentialVersion++ },
		func(e *platform.CapabilityEvidence) { e.AccountOwnerConfirmed = false },
	} {
		bad := b
		bad.PlatformGeneration = ""
		bad.CapabilityEvidence = slices.Clone(b.CapabilityEvidence)
		mutate(&bad.CapabilityEvidence[0])
		change := larkConfigurationChange(h)
		change.ExpectedBindingRevision = new(uint64(1))
		change.Binding = bad
		if r := managerHTTP(h.m, "/v1/tools/bind", change); r.Code != 400 {
			t.Fatal("client evidence was accepted", r.Code, r.Body.String())
		}
		h.m.mu.Lock()
		_, _, err = h.m.changeToolBindingLocked(t.Context(), change)
		h.m.mu.Unlock()
		if err == nil {
			t.Fatal("invalid server evidence committed")
		}
	}
	req = larkMemberToolRequest(t, h, 1)
	op, err = h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Result.Status != "preserved" {
		t.Fatal(op, err)
	}
	h.m.mu.Lock()
	f, err := h.m.readToolBindingsLocked(h.room.RoomIdentity)
	if err == nil {
		f.Bindings[0].CapabilityEvidence[0].CredentialVersion++
		err = h.m.store.save(h.m.toolBindingsPath(h.room.RoomIdentity), f, 1<<20)
	}
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	req = larkMemberToolRequest(t, h, 1)
	op, err = h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Result.Status != "applied" || op.Authorization.Revision != 2 || op.Capabilities.ActionRevision != 2 {
		t.Fatal("stale proof refresh did not produce matching revisions", op, err)
	}
	if configurationBindings(t, h).Bindings[0].Evidence("self_delivery").CredentialVersion != b.Evidence("self_delivery").CredentialVersion {
		t.Fatal("stale proof was reused")
	}
}

func TestLarkToolConfigurationOnboardingPreservesChoicesAndUnknownSubmission(t *testing.T) {
	parallelFixtureTest(t)
	h := larkToolConfigurationHarnessWithMembers(t, 1)
	change := larkConfigurationChange(h)
	h.m.mu.Lock()
	_, _, err := h.m.changeToolBindingLocked(t.Context(), change)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	req := larkOnboardingToolRequest(t, h)
	req.PreserveExisting = true
	h.m.mu.Lock()
	op, err := h.m.prepareOnboardingLocked(t.Context(), req)
	h.m.mu.Unlock()
	if err != nil || op.Tool.Status != "preserved" {
		t.Fatal(op.Tool, err)
	}
	if f := configurationBindings(t, h); f.Revision != 1 || f.Bindings[0].ActionAuthorization != nil {
		t.Fatal("preserve-existing expanded permissions")
	}
	req = larkOnboardingToolRequest(t, h)
	h.m.mu.Lock()
	op, err = h.m.prepareOnboardingLocked(t.Context(), req)
	h.m.mu.Unlock()
	if err != nil || op.Tool.Status != "pending" {
		t.Fatal(op.Tool, err)
	}
	original := h.m.store.ops
	h.m.store.ops.rename = func(from, to string) error {
		if to == h.m.onboardingPath(req.OperationID) {
			var candidate platformOnboarding
			raw, _ := os.ReadFile(from)
			if json.Unmarshal(raw, &candidate) == nil && candidate.Tool.Status == "applied" {
				return os.ErrPermission
			}
		}
		return original.rename(from, to)
	}
	_, err = h.m.applyOnboarding(t.Context(), req.OperationID, humanCaller.ID)
	if err == nil {
		t.Fatal("lost final receipt reported success")
	}
	h.m.store.ops = original
	before, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
	root := h.m.store.dir
	if err = h.m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, root, managerTestProfiles(t))
	op, err = reopened.applyOnboarding(t.Context(), req.OperationID, humanCaller.ID)
	if err != nil || op.Tool.Status != "unknown" {
		t.Fatal("unknown receipt was replayed", op.Tool, err)
	}
	after, _ := os.ReadFile(reopened.toolBindingsPath(h.room.RoomIdentity))
	if string(before) != string(after) {
		t.Fatal("unknown operation rewrote binding after restart")
	}
}

func TestLarkToolConfigurationRejectsActionsOutsideExplicitEnable(t *testing.T) {
	enabled, disabled := true, false
	for _, in := range []platformMemberInput{
		{PlatformID: "platform", PlatformVersion: 1, Enabled: &disabled, Actions: []string{"message.reply"}},
		{PlatformID: "platform", PlatformVersion: 1, Enabled: &enabled, Actions: []string{"future.write"}},
	} {
		if validPlatformMemberInput("tool", in) && validPlatformMemberProviderInput("lark", "tool", in) {
			t.Fatal("unsupported action input accepted", in)
		}
	}
	if validPlatformMemberProviderInput("gitlab", "tool", platformMemberInput{Actions: []string{"message.reply"}}) {
		t.Fatal("Lark action used on GitLab")
	}
}

func TestLarkToolConfigurationCLIAddsOnlyRequestedActions(t *testing.T) {
	parallelFixtureTest(t)
	h := larkToolConfigurationHarnessWithMembers(t, 1)
	req := larkMemberToolRequest(t, h, 0)
	client, endpoint := toolPipeServer(t, h.m)
	args := []string{"member", "tool", "--platform-id", h.c.ID, "--platform-version", strconv.FormatUint(h.c.Version, 10), "--binding-revision", "0", "--operation-id", req.OperationID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10), "--enabled", "true", "--action", "message.reply", "--action", "message.send", "--action", "message.reply"}
	var out, stderr bytes.Buffer
	if err := runPlatformConnectWithClient(t.Context(), args, &out, &stderr, cliToolEnv(endpoint, h.svc, h.members[0]), client); err != nil {
		t.Fatal(err, stderr.String())
	}
	var op platformMemberOperation
	if err := json.Unmarshal(out.Bytes(), &op); err != nil {
		t.Fatal(err)
	}
	if op.Status != "completed" || len(op.RequestedActions) != 2 || len(op.Authorization.Grants) != 2 || op.Authorization.Grants[1].Operation != "message.send" {
		t.Fatal(op)
	}
}

func TestLarkToolConfigurationCanonicalizesExistingGrants(t *testing.T) {
	parallelFixtureTest(t)
	h := larkToolConfigurationHarnessWithMembers(t, 1)
	change := larkConfigurationChange(h)
	caps, _ := h.m.referenceCapabilities("lark")
	grants := defaultLarkActionGrants(caps)
	slices.Reverse(grants)
	change.Binding.ActionAuthorization = &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: grants}
	h.m.mu.Lock()
	_, _, err := h.m.changeToolBindingLocked(t.Context(), change)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	req := larkMemberToolRequest(t, h, 1)
	op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Result.Status != "applied" || op.Result.Version != 2 || !slices.Equal(op.Authorization.Grants, defaultLarkActionGrants(caps)) {
		t.Fatal(op, err)
	}
}
