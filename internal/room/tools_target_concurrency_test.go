package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func configurationGitLab(t *testing.T, h *larkHarness) managedPlatform {
	t.Helper()
	h.m.mu.Lock()
	file, err := h.m.platforms.Load()
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Platforms {
		if c.Provider == "gitlab" {
			return managedPlatform{Platform: registry.Public(file, c)}
		}
	}
	t.Fatal("missing fixture GitLab")
	return managedPlatform{}
}

func TestToolIndependentTargetsMergeAndProjectActualCommit(t *testing.T) {
	for _, order := range []string{"lark-first", "gitlab-first", "overlap", "preserved-overlap"} {
		t.Run(order, func(t *testing.T) {
			h := larkToolConfigurationHarness(t)
			gitlab := configurationGitLab(t, h)
			larkReq := larkMemberToolRequest(t, h, 0)
			if order == "lark-first" {
				larkReq.Input.ExpectedBindingRevision = nil
			}
			// Both operations refer to the same actual human message and observe zero.
			gitlabReq := larkReq
			input := *larkReq.Input
			gitlabReq.Input = &input
			gitlabReq.OperationID, _ = newOpaqueID()
			input.PlatformID, input.PlatformVersion = gitlab.ID, gitlab.Version
			if order == "preserved-overlap" {
				op, err := h.m.configurePlatformMember(t.Context(), "tool", larkReq)
				if err != nil || op.Status != "completed" {
					t.Fatal(op, err)
				}
				larkReq = larkMemberToolRequest(t, h, 1)
			}
			applyGitLab := func() {
				t.Helper()
				op, err := h.m.configurePlatformMember(t.Context(), "tool", gitlabReq)
				if err != nil || op.Status != "completed" {
					t.Fatal(op, err)
				}
			}
			if strings.Contains(order, "overlap") {
				h.m.bindingEvidencePreparers["lark"] = func(context.Context, *toolBindingChange) error {
					applyGitLab()
					return errors.New("self proof unavailable")
				}
			} else if order == "gitlab-first" {
				applyGitLab()
			}
			op, err := h.m.configurePlatformMember(t.Context(), "tool", larkReq)
			if err != nil || op.Status != "completed" {
				t.Fatal(op, err)
			}
			if order == "lark-first" {
				applyGitLab()
			}
			f := configurationBindings(t, h)
			if len(f.Bindings) != 2 || f.Version != 2 || len(f.TargetRevisions) != 2 {
				t.Fatal(f)
			}
			if order == "overlap" && (op.Result.Version != 2 || op.Authorization.Revision != 2 || op.Capabilities.ToolRevision != 2 || op.Capabilities.ActionRevision != 2) {
				t.Fatal("receipt predicted rather than committed version", op)
			}
			if order == "preserved-overlap" && (op.Result.Status != "preserved" || op.Result.Version != 2 || op.Authorization.Revision != 1 || op.Capabilities.ToolRevision != 2) {
				t.Fatal("preserved returned stale file", op)
			}
			// An original-ID replay remains pure history after later target changes.
			before, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
			replay, err := h.m.configurePlatformMember(t.Context(), "tool", larkReq)
			after, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
			if err != nil || !sameJSON(replay, op) || !bytes.Equal(before, after) {
				t.Fatal("replayed intent", replay, err)
			}
		})
	}
}

func TestToolPreparedBindingRejectsABAAndAllowsOtherMembers(t *testing.T) {
	for _, mutation := range []string{"other-member", "aba"} {
		t.Run(mutation, func(t *testing.T) {
			h := larkToolConfigurationHarness(t)
			change := larkConfigurationChange(h)
			if err := h.m.prepareBindingEvidence(t.Context(), &change); err != nil {
				t.Fatal(err)
			}
			h.m.mu.Lock()
			other := larkConfigurationChange(h)
			other.ExpectedBindingRevision = nil
			if mutation == "other-member" {
				other.Binding.AgentID = h.members[1].AgentID
				other.Binding.MembershipID = h.members[1].MembershipID
			}
			_, _, err := h.m.changeToolBindingLocked(t.Context(), other)
			if err == nil && mutation == "aba" {
				other.Enabled = false
				other.PlatformVersion = 0
				_, _, err = h.m.changeToolBindingLocked(t.Context(), other)
				other.Enabled = true
				other.PlatformVersion = h.c.Version
				if err == nil {
					_, _, err = h.m.changeToolBindingLocked(t.Context(), other)
				}
			}
			if err != nil {
				h.m.mu.Unlock()
				t.Fatal(err)
			}
			next, _, err := h.m.changeToolBindingLocked(t.Context(), change)
			h.m.mu.Unlock()
			if mutation == "aba" {
				if !errors.Is(err, ErrConflict) {
					t.Fatal("ABA accepted", err)
				}
			} else if err != nil || len(next.Bindings) != 2 {
				t.Fatal(next, err)
			}
		})
	}
}

func TestToolBindingsLegacyUpgradeCapacityAndOverflow(t *testing.T) {
	for _, mode := range []string{"legacy", "capacity", "overflow", "invalid-marker", "file-size"} {
		t.Run(mode, func(t *testing.T) {
			h := larkToolConfigurationHarness(t)
			b := larkConfigurationChange(h).Binding
			f := toolBindingsFile{RoomIdentity: h.room.RoomIdentity, Version: 1, Revision: 7, Bindings: []tool.Binding{b}}
			h.m.mu.Lock()
			account, _, accountErr := h.m.resolvePlatformAccountLocked(h.room.RoomIdentity, h.c.ID, "lark")
			h.m.mu.Unlock()
			if accountErr != nil {
				t.Fatal(accountErr)
			}
			f.Bindings[0].PlatformGeneration = account.Generation
			if mode != "legacy" {
				f.Version = 2
				f.TargetRevisions = map[string]uint64{toolBindingKey(b): 7}
				f.Disabled = []tool.Binding{}
			}
			if mode == "capacity" {
				for i := 1; i < 3200; i++ {
					tomb := b
					tomb.AgentID = fmt.Sprint("retired-", i)
					f.TargetRevisions[toolBindingKey(tomb)] = 7
				}
			}
			if mode == "overflow" {
				f.Revision = ^uint64(0)
			}
			if mode == "invalid-marker" {
				f.TargetRevisions[toolBindingKey(b)] = 8
			}
			writeJSONFixture(t, h.m.toolBindingsPath(h.room.RoomIdentity), f)
			before, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
			h.m.mu.Lock()
			read, err := h.m.readToolBindingsLocked(h.room.RoomIdentity)
			h.m.mu.Unlock()
			if mode == "invalid-marker" {
				if !errors.Is(err, ErrInvalid) {
					t.Fatal(read, err)
				}
				return
			}
			if err != nil || read.TargetRevisions[toolBindingKey(b)] != 7 {
				t.Fatal(read, err)
			}
			after, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
			if !bytes.Equal(before, after) {
				t.Fatal("read migrated file")
			}
			change := larkConfigurationChange(h)
			change.Enabled = false
			change.PlatformVersion = 0
			change.ExpectedBindingRevision = new(uint64(7))
			if mode == "file-size" {
				change.Binding.AgentID = strings.Repeat("x", 600000)
				change.ExpectedBindingRevision = new(uint64(0))
			}
			if mode == "capacity" {
				change.Binding.AgentID = h.members[1].AgentID
				change.Binding.MembershipID = h.members[1].MembershipID
				change.ExpectedBindingRevision = new(uint64(0))
			}
			h.m.mu.Lock()
			next, _, err := h.m.changeToolBindingLocked(t.Context(), change)
			h.m.mu.Unlock()
			if mode == "legacy" {
				if err != nil || next.Version != 2 || next.TargetRevisions[toolBindingKey(b)] != 8 || !next.LegacyUnknown {
					t.Fatal(next, err)
				}
			} else {
				if mode == "file-size" {
					if err == nil || !strings.Contains(err.Error(), "snapshot byte capacity") {
						t.Fatalf("expected file size limit: %v", err)
					}
				} else if !errors.Is(err, ErrCapacity) {
					t.Fatal(next, err)
				}
				after, _ = os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
				if !bytes.Equal(before, after) {
					t.Fatal("failed save changed file")
				}
			}
		})
	}
}

func TestToolContractsRejectRetiredVersionsAndRequireHumanObservation(t *testing.T) {
	h := larkToolConfigurationHarness(t)
	change := larkConfigurationChange(h)
	change.ExpectedBindingRevision = nil
	data, _ := json.Marshal(change)
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	delete(raw, "expected_binding_revision")
	if w := managerHTTP(h.m, "/v1/tools/bind", raw); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, value := range []any{0, 1, nil} {
		raw["revision"] = value
		if w := managerHTTP(h.m, "/v1/tools/bind", raw); w.Code != 400 || !strings.Contains(w.Body.String(), "retired") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	req := larkMemberToolRequest(t, h, 0)
	data, _ = json.Marshal(req)
	_ = json.Unmarshal(data, &raw)
	raw["input"].(map[string]any)["tool_revision"] = 0
	if w := managerHTTP(h.m, "/v1/platforms/member/tool", raw); w.Code != 400 || !strings.Contains(w.Body.String(), "retired") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestToolSaveAttemptBoundaryAndOriginalOperationFence(t *testing.T) {
	for _, mode := range []string{"before-save", "save-attempt", "receipt-loss"} {
		t.Run(mode, func(t *testing.T) {
			h := larkToolConfigurationHarness(t)
			req := larkMemberToolRequest(t, h, 0)
			real := h.m.store.ops
			if mode == "before-save" {
				req.Input.ExpectedBindingRevision = new(uint64(9))
			} else {
				h.m.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					if mode == "save-attempt" && dir == h.m.roomDir(h.room.RoomID) {
						return nil, os.ErrPermission
					}
					return real.createTemp(dir, pattern)
				}
				h.m.store.ops.rename = func(from, to string) error {
					if mode == "receipt-loss" && to == h.m.platformMemberPath(req.OperationID) {
						var op platformMemberOperation
						data, _ := os.ReadFile(from)
						if json.Unmarshal(data, &op) == nil && op.Status == "completed" {
							return os.ErrPermission
						}
					}
					return real.rename(from, to)
				}
			}
			op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
			if mode == "before-save" {
				var d *requestDiagnostic
				if !errors.Is(err, ErrConflict) || !errors.As(err, &d) || d.Outcome != "not_applied" {
					t.Fatal(op, err)
				}
				return
			}
			if mode == "save-attempt" && (err != nil || op.Status != "unknown") {
				t.Fatal(op, err)
			}
			if mode == "receipt-loss" && err == nil {
				t.Fatal("lost receipt reported success")
			}
			h.m.store.ops = real
			// Restart clears Store's uncertain-write latch, never the unknown fence.
			if mode == "receipt-loss" {
				if err := h.m.close(); err != nil {
					t.Fatal(err)
				}
				h.m = testManager(t, h.m.store.dir, nil)
				h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
				req.ServiceInfo = h.svc.info
			}
			// The historical unknown is the replay fence.
			prior, err := h.m.readPlatformMemberLocked(req.OperationID)
			if err != nil || prior.Status != "unknown" {
				t.Fatal(prior, err)
			}
			before, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
			replay, err := h.m.configurePlatformMember(t.Context(), "tool", req)
			after, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
			if err != nil || replay.Status != "unknown" || !bytes.Equal(before, after) {
				t.Fatal(replay, err)
			}
		})
	}
}

func TestLarkPermissionGapRechecksExactTargetAfterRemoteRead(t *testing.T) {
	for _, mutation := range []string{"sibling", "same-target"} {
		t.Run(mutation, func(t *testing.T) {
			h := larkToolConfigurationHarness(t)
			writeJSONFixture(t, h.dir+"/missing-scopes.json", []string{"im:message:readonly"})
			script, err := os.ReadFile(h.dir + "/lark-cli")
			if err != nil {
				t.Fatal(err)
			}
			gate := `if a==['auth','scopes','--json']:
 import time
 open(base+'/permission-ready','w').close()
 while not os.path.exists(base+'/permission-release'):time.sleep(0.01)
`
			script = bytes.Replace(script, []byte("if a==['auth','scopes','--json']:\n"), []byte(gate), 1)
			if err = os.WriteFile(h.dir+"/lark-cli", script, 0700); err != nil {
				t.Fatal(err)
			}
			change := larkConfigurationChange(h)
			change.ExpectedBindingRevision = nil
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			go func() { done <- h.m.prepareBindingEvidence(ctx, &change) }()
			for {
				if _, err = os.Stat(h.dir + "/permission-ready"); err == nil {
					break
				}
				select {
				case err := <-done:
					t.Fatal("permission read did not block", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			other := larkConfigurationChange(h)
			other.ExpectedBindingRevision = nil
			if mutation == "same-target" {
				other.Enabled = false
				other.PlatformVersion = 0
			} else {
				g := configurationGitLab(t, h)
				other.Binding.Tool = "gitlab"
				other.Binding.PlatformID = g.ID
				other.PlatformVersion = g.Version
			}
			h.m.mu.Lock()
			_, _, err = h.m.changeToolBindingLocked(t.Context(), other)
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(h.dir+"/permission-release", nil, 0600); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if mutation == "same-target" {
				if !errors.Is(err, ErrConflict) && !errors.Is(err, context.Canceled) {
					t.Fatal("stale permission gap accepted", err)
				}
			} else {
				var required *userAuthorizationRequired
				if !errors.As(err, &required) || required.Capabilities.ToolRevision != 1 {
					t.Fatal("sibling blocked permission result or stale projection", err)
				}
			}
		})
	}
}

func TestOnboardingToolExactObservationAndLegacyPending(t *testing.T) {
	for _, mode := range []string{"independent", "legacy-pending", "receipt-loss"} {
		t.Run(mode, func(t *testing.T) {
			h := larkToolConfigurationHarness(t)
			g := configurationGitLab(t, h)
			req := larkOnboardingToolRequest(t, h)
			req.Provider = "gitlab"
			req.Origin = g.URL
			req.PlatformID = g.ID
			h.m.mu.Lock()
			op, err := h.m.prepareOnboardingLocked(t.Context(), req)
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if op.Schema != 4 || op.BeforeBindingRevision == nil || *op.BeforeBindingRevision != 0 {
				t.Fatal(op)
			}
			if mode == "independent" {
				bindCLITool(t, h.m, h.room.RoomIdentity, h.members[1], g, "gitlab", 0)
			}
			if mode == "legacy-pending" {
				op.Schema = 3
				op.BeforeBindingRevision = nil
				h.m.mu.Lock()
				err = h.m.saveOnboardingLocked(op)
				h.m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			real := h.m.store.ops
			if mode == "receipt-loss" {
				h.m.store.ops.rename = func(from, to string) error {
					if to == h.m.onboardingPath(op.ID) {
						var saved platformOnboarding
						raw, _ := os.ReadFile(from)
						if json.Unmarshal(raw, &saved) == nil && saved.Tool.Status == "applied" {
							return os.ErrPermission
						}
					}
					return real.rename(from, to)
				}
			}
			applied, err := h.m.applyOnboarding(t.Context(), op.ID, humanCaller.ID)
			if mode == "independent" {
				if err != nil || applied.Tool.Status != "applied" || applied.Tool.Version != 2 {
					t.Fatal(applied, err)
				}
				f := configurationBindings(t, h)
				if len(f.Bindings) != 2 {
					t.Fatal(f)
				}
			}
			if mode == "legacy-pending" {
				if err != nil || applied.Tool.Status != "failed" || applied.Tool.ErrorCode != "binding_observation_required" {
					t.Fatal(applied, err)
				}
				if len(configurationBindings(t, h).Bindings) != 0 {
					t.Fatal("legacy pending replayed")
				}
			}
			if mode == "receipt-loss" {
				if err == nil {
					t.Fatal("lost receipt reported success")
				}
				h.m.store.ops = real
				if err = h.m.close(); err != nil {
					t.Fatal(err)
				}
				h.m = testManager(t, h.m.store.dir, nil)
				before, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
				replay, err := h.m.applyOnboarding(t.Context(), op.ID, humanCaller.ID)
				after, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
				if err != nil || replay.Tool.Status != "unknown" || !bytes.Equal(before, after) {
					t.Fatal("inferred success or replayed unknown", replay, err)
				}
			}
		})
	}
}

func TestToolIntentIdentityIncludesObservationAndNormalizesActions(t *testing.T) {
	h := larkToolConfigurationHarness(t)
	req := larkMemberToolRequest(t, h, 0)
	req.Input.Actions = []string{"message.send", "message.reply"}
	op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Status != "completed" {
		t.Fatal(op, err)
	}
	req.Input.Actions = []string{"message.reply", "message.send", "message.reply"}
	replay, err := h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || !sameJSON(op, replay) {
		t.Fatal("action order changed identity", replay, err)
	}
	req.Input.ExpectedBindingRevision = nil
	if _, err = h.m.configurePlatformMember(t.Context(), "tool", req); !errors.Is(err, ErrConflict) {
		t.Fatal("observation was removed under original ID", err)
	}
	// A later recorded intent prevents a fresh ID from executing an older source.
	newer := larkMemberToolRequest(t, h, 1)
	newer.Input.Actions = []string{"message.reply"}
	if _, err = h.m.configurePlatformMember(t.Context(), "tool", newer); err != nil {
		t.Fatal(err)
	}
	req.OperationID, _ = newOpaqueID()
	h.m.bindingEvidencePreparers["lark"] = func(context.Context, *toolBindingChange) error { t.Error("old source reached preparation"); return nil }
	_, err = h.m.configurePlatformMember(t.Context(), "tool", req)
	var diagnostic *requestDiagnostic
	if !errors.Is(err, ErrConflict) || !errors.As(err, &diagnostic) || (diagnostic.OperationID != newer.OperationID && diagnostic.OperationID != op.ID) {
		t.Fatal(err)
	}
}

func TestToolDefaultActionIntentSurvivesOmittedJSONFields(t *testing.T) {
	h := larkToolConfigurationHarness(t)
	req := larkMemberToolRequest(t, h, 0)
	req.Input.Actions = []string{}
	op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || op.Status != "completed" {
		t.Fatal(op, err)
	}
	replay, err := h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || !sameJSON(replay, op) {
		t.Fatal("empty default plan could not recover its receipt", replay, err)
	}
	req.Input.Actions = nil
	replay, err = h.m.configurePlatformMember(t.Context(), "tool", req)
	if err != nil || !sameJSON(replay, op) {
		t.Fatal("omission changed the default plan", replay, err)
	}
}

func TestOnboardingToolInterruptedSettlementKeepsCommittedProjection(t *testing.T) {
	h := larkToolConfigurationHarness(t)
	bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], h.c, "lark", 0)
	req := larkOnboardingToolRequest(t, h)
	h.m.mu.Lock()
	prepared, err := h.m.prepareOnboardingLocked(t.Context(), req)
	h.m.mu.Unlock()
	if err != nil || prepared.Tool.Status != "pending" {
		t.Fatal(prepared.Tool, err)
	}
	// An unrelated commit makes a predicted before+1 authorization incorrect.
	bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], configurationGitLab(t, h), "gitlab", 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	call := &platformCall{owner: h.room.RoomIdentity, agentID: h.members[0].AgentID, membershipID: h.members[0].MembershipID, tool: "lark", platformID: h.c.ID, cancel: cancel, done: make(chan struct{})}
	h.m.mu.Lock()
	if h.m.platformCalls == nil {
		h.m.platformCalls = map[*platformCall]bool{}
	}
	h.m.platformCalls[call] = true
	h.m.mu.Unlock()
	defer h.m.finishPlatformCall(call)
	result, err := h.m.applyOnboarding(ctx, req.OperationID, humanCaller.ID)
	if err != nil || result.Tool.Status != "unknown" || result.Tool.Authorization == nil || result.Tool.Authorization.Revision != 3 || result.Tool.Capabilities == nil || result.Tool.Capabilities.ActionRevision != 3 || result.Tool.Capabilities.ToolRevision != 3 {
		t.Fatal(result.Tool, err)
	}
	stored, err := h.m.readOnboardingLocked(req.OperationID, humanCaller.ID)
	if err != nil || stored.Tool.Status != "unknown" || !validOnboarding(stored) {
		t.Fatal(stored.Tool, err)
	}
	before := configurationBindings(t, h)
	replay, err := h.m.applyOnboarding(t.Context(), req.OperationID, humanCaller.ID)
	if err != nil || replay.Tool.Status != "unknown" || !sameJSON(before, configurationBindings(t, h)) {
		t.Fatal("settlement retry replayed the commit", replay.Tool, err)
	}
}

func TestToolPreparationRejectsExpiredCapabilityObservation(t *testing.T) {
	h := larkToolConfigurationHarness(t)
	change := larkConfigurationChange(h)
	if err := h.m.prepareBindingEvidence(t.Context(), &change); err != nil {
		t.Fatal(err)
	}
	change.prepared.Capabilities.CheckedAt = time.Now().Add(-larkCapabilityMaxAge - time.Second)
	h.m.mu.Lock()
	_, _, err := h.m.changeToolBindingLocked(t.Context(), change)
	h.m.mu.Unlock()
	var d *requestDiagnostic
	if !errors.Is(err, ErrConflict) || !errors.As(err, &d) || d.Field != "capabilities" {
		t.Fatal(err)
	}
	if configurationBindings(t, h).Revision != 0 {
		t.Fatal("expired observation was saved")
	}
}

func TestToolLegacyUnknownReceiptsRemainReadOnlyAfterRestart(t *testing.T) {
	for _, provider := range []string{"lark", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			h := larkToolConfigurationHarness(t)
			req := larkMemberToolRequest(t, h, 0)
			req.Input.ExpectedBindingRevision = nil
			if provider == "gitlab" {
				g := configurationGitLab(t, h)
				req.Input.PlatformID, req.Input.PlatformVersion = g.ID, g.Version
			}
			op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
			if err != nil || op.Status != "completed" {
				t.Fatal(op, err)
			}
			op.Schema = 1
			if provider == "lark" {
				op.Schema = 2
			}
			op.Status, op.ResultVersion = "unknown", 0
			op.Result = onboardingStep{Status: "unknown", ErrorCode: "submission_unconfirmed"}
			h.m.mu.Lock()
			err = h.m.savePlatformMemberLocked(op)
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
			if err = h.m.close(); err != nil {
				t.Fatal(err)
			}
			h.m = testManager(t, h.m.store.dir, nil)
			h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
			req.ServiceInfo = h.svc.info
			replay, err := h.m.configurePlatformMember(t.Context(), "tool", req)
			after, _ := os.ReadFile(h.m.toolBindingsPath(h.room.RoomIdentity))
			if err != nil || !sameJSON(replay, op) || !bytes.Equal(before, after) {
				t.Fatal("legacy unknown was reinterpreted", replay, err)
			}
		})
	}
}
