package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func currentMemberRequest(req platformMemberRequest) platformMemberRequest {
	req.OperationID, req.ObserveCurrent = "", true
	req.Input = &platformMemberInput{PlatformID: req.Input.PlatformID, Enabled: req.Input.Enabled}
	return req
}

// Preserve wire-level field presence, including deliberately invalid zero/nulls.
func currentMemberWire(t *testing.T, req platformMemberRequest) map[string]any {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	input := wire["input"].(map[string]any)
	delete(input, "platform_version")
	delete(input, "notification_version")
	return wire
}

func applyCurrentMember(t *testing.T, m *roomManager, req platformMemberRequest) platformMemberOperation {
	t.Helper()
	w := managerHTTP(m, "/v1/platforms/member/notifications", currentMemberWire(t, req))
	var op platformMemberOperation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &op) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return op
}

func TestPlatformMemberCurrentCLIUsesOneBusinessRequest(t *testing.T) {
	parallelFixtureTest(t)
	for _, provider := range []string{"lark", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			m, svc, req := memberNotificationToggleFixture(t, provider)
			client, endpoint := toolPipeServer(t, m)
			transport := client.Transport
			var paths []string
			var inputs []map[string]any
			client.Transport = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/platforms/member/") {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						return nil, err
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					var wire map[string]any
					if err := json.Unmarshal(body, &wire); err != nil {
						return nil, err
					}
					paths, inputs = append(paths, r.URL.Path), append(inputs, wire)
				}
				return transport.RoundTrip(r)
			})
			env := cliToolEnv(endpoint, svc, MemberState{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID})
			toggles := []bool{false, true, false, true}
			if provider == "gitlab" {
				toggles = []bool{true}
			}
			for i, enabled := range toggles {
				if i != 0 {
					req = nextMemberNotificationToggle(t, svc, req, 0, enabled)
				}
				var out bytes.Buffer
				paths, inputs = nil, nil
				args := []string{"member", "notifications", "--platform-id", req.Input.PlatformID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10), "--enabled", strconv.FormatBool(enabled)}
				if err := runPlatformConnectWithClient(t.Context(), args, &out, &out, env, client); err != nil {
					t.Fatal(err, out.String())
				}
				if !reflect.DeepEqual(paths, []string{"/v1/platforms/member/notifications"}) {
					t.Fatal("extra business request", paths)
				}
				input := inputs[0]["input"].(map[string]any)
				if len(input) != 2 || input["enabled"] != enabled || inputs[0]["observe_current"] != true || inputs[0]["operation_id"] != nil {
					t.Fatal(inputs)
				}
				var op platformMemberOperation
				if err := json.Unmarshal(out.Bytes(), &op); err != nil {
					t.Fatal(err)
				}
				if op.Status != "completed" || op.Source.Seq != req.SourceSeq || op.Input.PlatformVersion != req.Input.PlatformVersion || op.ID == "" || !op.Input.pureNotificationToggle() {
					t.Fatal(op)
				}
				if i == 0 && provider == "lark" && (op.Result.Status != "preserved" || op.Result.Version != 0) {
					t.Fatal(op)
				}
				if (i > 0 || provider == "gitlab") && (op.Result.Status != "applied" || op.Input.NotificationVersion != uint64(max(0, i-1)) || op.Result.Version != uint64(max(1, i))) {
					t.Fatal(op)
				}
				// Losing the response still recovers exactly the original frozen input.
				out.Reset()
				if err := runPlatformConnectWithClient(t.Context(), args, &out, &out, env, client); err != nil {
					t.Fatal(err)
				}
				var replay platformMemberOperation
				if json.Unmarshal(out.Bytes(), &replay) != nil || !reflect.DeepEqual(replay, op) {
					t.Fatal("replay changed receipt", out.String())
				}
			}
			paths, inputs = nil, nil
			var out bytes.Buffer
			if err := runPlatformConnectWithClient(t.Context(), []string{"member", "get", "--platform-id", req.Input.PlatformID}, &out, &out, env, client); err != nil {
				t.Fatal(err, out.String())
			}
			if !reflect.DeepEqual(paths, []string{"/v1/platforms/member/get"}) || len(inputs[0]["input"].(map[string]any)) != 1 {
				t.Fatal(paths, inputs)
			}
		})
	}
}

func TestPlatformMemberCurrentCLIRejectsMixedModes(t *testing.T) {
	base := []string{"member", "notifications", "--platform-id", strings.Repeat("a", 32), "--source-seq", "1", "--enabled", "false"}
	for _, extra := range [][]string{
		{"--notification-version", "0"}, {"--platform-version", "0"}, {"--operation-id", ""},
		{"--add-chat-id", ""}, {"--todo-reason", ""}, {"--project-id", "0"}, {"--group-enabled", "false"},
		{"--platform-version", "1", "--operation-id", strings.Repeat("b", 32)},
	} {
		var out bytes.Buffer
		err := runPlatformMemberCLI(t.Context(), append(append([]string{}, base...), extra...), &out, &out, func(string) string { t.Fatal("invalid input reached identity/network"); return "" }, nil)
		if err == nil {
			t.Fatal("mixed mode accepted", extra)
		}
	}
	for _, args := range [][]string{
		{"member", "get", "--platform-id", strings.Repeat("a", 32), "--platform-version", "0"},
		{"member", "notifications", "--platform-id", strings.Repeat("a", 32), "--source-seq", "1"},
	} {
		var out bytes.Buffer
		if err := runPlatformMemberCLI(t.Context(), args, &out, &out, func(string) string { t.Fatal("invalid input reached identity/network"); return "" }, nil); err == nil {
			t.Fatal(args)
		}
	}
}

func TestPlatformMemberCurrentHTTPRejectsExpandedShapes(t *testing.T) {
	parallelFixtureTest(t)
	m, svc, req := memberNotificationToggleFixture(t, "lark")
	req = currentMemberRequest(req)
	read := req
	read.SourceSeq, read.Input = 0, &platformMemberInput{PlatformID: req.Input.PlatformID}
	w := managerHTTP(m, "/v1/platforms/member/get", currentMemberWire(t, read))
	var v platformMemberView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.NotificationVersion != 0 || !v.Baseline.IsZero() {
		t.Fatal("current read created configuration or reception start", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		path, field string
		read        bool
	}{
		{"/v1/platforms/member/get", "platform_version", true},
		{"/v1/platforms/member/notifications", "notification_version", false},
	} {
		request := req
		if tc.read {
			request = read
		}
		wire := currentMemberWire(t, request)
		wire["input"].(map[string]any)[tc.field] = 0
		if w := managerHTTP(m, tc.path, wire); w.Code != 400 {
			t.Fatal("expanded current request accepted", tc.field, w.Code, w.Body.String())
		}
	}
	for _, enabled := range []any{nil, "false"} {
		wire := currentMemberWire(t, req)
		wire["input"].(map[string]any)["enabled"] = enabled
		if w := managerHTTP(m, "/v1/platforms/member/notifications", wire); w.Code != 400 {
			t.Fatal(enabled, w.Code, w.Body.String())
		}
	}
	posted, err := svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: req.Caller, ClientMessageID: "fabricated-toggle", Text: "开启通知"})
	if err != nil {
		t.Fatal(err)
	}
	for _, seq := range []uint64{0, posted.Seq, posted.Seq + 100} {
		bad := req
		bad.SourceSeq = seq
		if w := managerHTTP(m, "/v1/platforms/member/notifications", currentMemberWire(t, bad)); w.Code == 200 {
			t.Fatal("nonhuman source accepted", seq)
		}
	}
	m.mu.Lock()
	ops, err := m.platformMemberOperationsLocked()
	m.mu.Unlock()
	if err != nil || len(ops) != 0 {
		t.Fatal("invalid requests created receipts", ops, err)
	}
}

func TestPlatformMemberCurrentReadAndHistoricalReceipt(t *testing.T) {
	parallelFixtureTest(t)
	for _, provider := range []string{"lark"} {
		t.Run(provider, func(t *testing.T) {
			m, svc, full := memberNotificationToggleFixture(t, provider)
			req := currentMemberRequest(full)
			op := applyCurrentMember(t, m, req)
			// A newer operation changes current settings; the old result stays historical.
			later := currentMemberRequest(nextMemberNotificationToggle(t, svc, full, 0, false))
			applyCurrentMember(t, m, later)
			m.mu.Lock()
			file, err := m.platforms.Load()
			if err == nil {
				for i := range file.Platforms {
					if file.Platforms[i].ID == req.Input.PlatformID {
						file.Platforms[i].Version++
					}
				}
				err = m.store.save(m.platformsPathForTest(), file, 2<<20)
			}
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if replay := applyCurrentMember(t, m, req); !reflect.DeepEqual(replay, op) {
				t.Fatal("newer source/version invalidated historical receipt", replay)
			}
			read := platformMemberRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, ObserveCurrent: true, Input: &platformMemberInput{PlatformID: req.Input.PlatformID}}
			w := managerHTTP(m, "/v1/platforms/member/get", currentMemberWire(t, read))
			var v platformMemberView
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.PlatformVersion != full.Input.PlatformVersion+1 || v.NotificationVersion != 2 {
				t.Fatal(w.Code, w.Body.String())
			}
			read.ObserveCurrent, read.Input.PlatformVersion = false, full.Input.PlatformVersion
			if w := managerHTTP(m, "/v1/platforms/member/get", read); w.Code != 409 {
				t.Fatal("explicit stale version accepted", w.Code, w.Body.String())
			}
			bad := req
			input := *req.Input
			bad.Input = &input
			input.Enabled = boolPointer(false)
			if platformMemberToggleID(bad) != op.ID {
				t.Fatal("boolean changed stable ID")
			}
			if w := managerHTTP(m, "/v1/platforms/member/notifications", currentMemberWire(t, bad)); w.Code != 409 {
				t.Fatal("opposite boolean accepted", w.Code, w.Body.String())
			}
			m.mu.Lock()
			plan, err := m.platforms.Plan(registry.Request{ID: req.Input.PlatformID, Version: full.Input.PlatformVersion + 1}, true)
			if err == nil {
				_, err = m.platforms.Commit(plan)
			}
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if replay := applyCurrentMember(t, m, req); !reflect.DeepEqual(replay, op) {
				t.Fatal("removed account invalidated receipt")
			}
			if _, err := svc.remove(apiRequest{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}); err != nil {
				t.Fatal(err)
			}
			if w := managerHTTP(m, "/v1/platforms/member/notifications", currentMemberWire(t, req)); w.Code == 200 {
				t.Fatal("removed member recovered receipt")
			}
		})
	}
}

func TestPlatformMemberCurrentSourceFences(t *testing.T) {
	parallelFixtureTest(t)
	for _, complex := range []bool{false, true} {
		m, svc, full := memberNotificationToggleFixture(t, "lark")
		if complex {
			full.Input.Enabled = nil
			full.Input.Groups = &platformMemberGroupPatch{Enabled: boolPointer(true), Scope: "all"}
		}
		original := gitlabMemberApply(t, m, "notifications", full)
		req := currentMemberRequest(full)
		req.Input.Enabled = boolPointer(true)
		if w := managerHTTP(m, "/v1/platforms/member/notifications", currentMemberWire(t, req)); w.Code != 409 || !strings.Contains(w.Body.String(), original.ID) {
			t.Fatal("parallel same-source operation", w.Code, w.Body.String())
		}
		later := currentMemberRequest(nextMemberNotificationToggle(t, svc, full, 0, false))
		newer := applyCurrentMember(t, m, later)
		if w := managerHTTP(m, "/v1/platforms/member/notifications", currentMemberWire(t, req)); w.Code != 409 {
			t.Fatal("older source bypassed newer receipt", newer, w.Code, w.Body.String())
		}
	}
	m, svc, full := memberNotificationToggleFixture(t, "lark")
	older := currentMemberRequest(full)
	later := currentMemberRequest(nextMemberNotificationToggle(t, svc, full, 0, true))
	newer := applyCurrentMember(t, m, later)
	if w := managerHTTP(m, "/v1/platforms/member/notifications", currentMemberWire(t, older)); w.Code != 409 || !strings.Contains(w.Body.String(), newer.ID) {
		t.Fatal("unsubmitted older source bypassed newer receipt", w.Code, w.Body.String())
	}
}

func TestPlatformMemberCurrentAdoptsSavedScopeBeforeAdmission(t *testing.T) {
	parallelFixtureTest(t)
	for _, provider := range []string{"lark", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			m, _, full := memberNotificationToggleFixture(t, provider)
			baseline := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
			if provider == "lark" {
				cfg := m.defaultLarkConfig(full.Input.PlatformID)
				cfg.Target, cfg.Enabled, cfg.IntervalSeconds, cfg.BackfillStart = platformMemberTarget(full.member()), false, 61, baseline
				cfg.Private = lark.PrivateConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_person"}}
				cfg.Groups = lark.GroupConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_group"}, MessageMode: "mentions", AtAll: true}
				if _, err := m.configureLarkSubscription(t.Context(), notification.Key{Owner: full.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg); err != nil {
					t.Fatal(err)
				}
			} else {
				cfg := gitlab.DefaultNotificationConfig(full.Input.PlatformID)
				cfg.Target, cfg.Enabled, cfg.IntervalSeconds, cfg.BackfillStart = platformMemberTarget(full.member()), false, 61, baseline
				cfg.Todos.Filter = gitlab.TodosFilter{Reasons: []string{"mentioned"}, Types: []string{"work_item"}}
				if _, err := m.configureGitLabNotifications(t.Context(), full.RoomIdentity, 0, cfg); err != nil {
					t.Fatal(err)
				}
			}
			before := gitlabMemberGet(t, m, full)
			// Complete mode remains conditional on its original version=0.
			if w := managerHTTP(m, "/v1/platforms/member/notifications", full); w.Code != 409 {
				t.Fatal("complete stale observation accepted", w.Code, w.Body.String())
			}
			op := applyCurrentMember(t, m, currentMemberRequest(full))
			after := gitlabMemberGet(t, m, full)
			if op.Status != "completed" || op.Input.NotificationVersion != 1 || after.NotificationVersion != 2 || after.BindingRevision != before.BindingRevision {
				t.Fatal(op, after)
			}
			if provider == "lark" {
				want := *before.Config
				want.Enabled = true
				if !reflect.DeepEqual(want, *after.Config) {
					t.Fatal("replaced current scope with defaults", before.Config, after.Config)
				}
			} else {
				want := before.GitLab.Config
				want.Enabled = true
				// First actual reception locks the already selected absolute start.
				if !reflect.DeepEqual(want, after.GitLab.Config) {
					t.Fatal("replaced current filters with defaults", want, after.GitLab.Config)
				}
			}
		})
	}
}

// Block immediately after reservation and before entering a configuration service.
// This uses the real persistence seam and context cancellation check, with no
// production test hooks or simulated apply result.
type reservedMemberContext struct {
	context.Context
	reserved, entered, release chan struct{}
	once                       sync.Once
}

func (c *reservedMemberContext) Err() error {
	select {
	case <-c.reserved:
		c.once.Do(func() { close(c.entered); <-c.release })
	default:
	}
	return c.Context.Err()
}

func TestPlatformMemberCurrentConcurrentReservationAndFrozenAdmission(t *testing.T) {
	parallelFixtureTest(t)
	for _, provider := range []string{"lark", "gitlab"} {
		for _, change := range []string{"none", "config", "account", "member", "subscription"} {
			t.Run(provider+"/"+change, func(t *testing.T) {
				if provider == "gitlab" && change == "subscription" {
					return
				} // Lark has the separate subscription identity guard.
				m, svc, full := memberNotificationToggleFixture(t, provider)
				observedVersion := uint64(0)
				if change == "subscription" {
					gitlabMemberApply(t, m, "notifications", full)
					full = nextMemberNotificationToggle(t, svc, full, 1, false)
					observedVersion = 1
				}
				req := currentMemberRequest(full)
				ctx := &reservedMemberContext{Context: t.Context(), reserved: make(chan struct{}), entered: make(chan struct{}), release: make(chan struct{})}
				var release sync.Once
				defer release.Do(func() { close(ctx.release) })
				real := m.store.ops
				m.mu.Lock()
				m.store.ops.rename = func(from, to string) error {
					if err := real.rename(from, to); err != nil {
						return err
					}
					if to == m.platformMemberPath(platformMemberToggleID(req)) {
						select {
						case <-ctx.reserved:
						default:
							close(ctx.reserved)
						}
					}
					return nil
				}
				m.mu.Unlock()
				type result struct {
					op  platformMemberOperation
					err error
				}
				done := make(chan result, 1)
				go func() { op, err := m.configurePlatformMember(ctx, "notifications", req); done <- result{op, err} }()
				select {
				case <-ctx.entered:
				case <-time.After(10 * time.Second):
					t.Fatal("did not reach saved reservation")
				}
				// Real concurrent request sees the saved fence and cannot submit twice.
				pending := applyCurrentMember(t, m, req)
				if pending.Status != "unknown" || pending.Input.NotificationVersion != observedVersion || pending.Input.PlatformVersion != full.Input.PlatformVersion {
					t.Fatal(pending)
				}
				m.mu.Lock()
				m.store.ops = real
				m.mu.Unlock()
				switch change {
				case "config":
					// Human management saves a competing current configuration while the
					// operation is stopped outside the manager lock.
					if provider == "lark" {
						cfg := m.defaultLarkConfig(req.Input.PlatformID)
						cfg.Target = platformMemberTarget(req.member())
						cfg.Enabled = false
						if _, err := m.configureLarkSubscription(t.Context(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg); err != nil {
							t.Fatal(err)
						}
					} else {
						cfg := *pending.GitLabConfig
						cfg.Enabled = false
						if _, err := m.configureGitLabNotifications(t.Context(), req.RoomIdentity, 0, cfg); err != nil {
							t.Fatal(err)
						}
					}
				case "account":
					m.mu.Lock()
					file, err := m.platforms.Load()
					if err == nil {
						for i := range file.Platforms {
							if file.Platforms[i].ID == req.Input.PlatformID {
								file.Platforms[i].Version++
							}
						}
						err = m.store.save(m.platformsPathForTest(), file, 2<<20)
					}
					m.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
				case "subscription":
					m.mu.Lock()
					old, err := m.currentLarkForTargetLocked(req.RoomIdentity, req.Input.PlatformID, platformMemberTarget(req.member()))
					if err == nil {
						retired, e := lark.Retire(*old)
						err = e
						if err == nil {
							err = m.saveNotificationLocked(retired, *old)
						}
						if err == nil {
							replacement := old.Clone()
							replacement.SubscriptionID, err = newOpaqueID()
							if err == nil {
								err = m.saveNotificationLocked(replacement, nil)
							}
						}
					}
					m.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
				case "member":
					if _, err := svc.remove(apiRequest{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}); err != nil {
						t.Fatal(err)
					}
				}
				release.Do(func() { close(ctx.release) })
				var got result
				select {
				case got = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("apply did not finish")
				}
				if got.err != nil {
					t.Fatal(got.err)
				}
				want := "unknown"
				if change == "none" {
					want = "completed"
				}
				if got.op.Status != want || !reflect.DeepEqual(got.op.Input, pending.Input) {
					t.Fatal("refreshed frozen input", got.op)
				}
				if change != "member" {
					if replay := applyCurrentMember(t, m, req); !reflect.DeepEqual(replay, got.op) {
						t.Fatal("replayed fenced operation")
					}
					read := full
					if change == "account" {
						read.Input.PlatformVersion++
					}
					v := gitlabMemberGet(t, m, read)
					wantVersion := uint64(0)
					if change == "none" || change == "config" || change == "subscription" {
						wantVersion = 1
					}
					if v.NotificationVersion != wantVersion {
						t.Fatal("stale admission wrote configuration", v)
					}
					if change == "subscription" && !v.Config.Enabled {
						t.Fatal("replaced subscription was disabled by stale admission", v)
					}
					if change == "config" && (v.Config != nil && v.Config.Enabled || v.GitLab != nil && v.GitLab.Config.Enabled) {
						t.Fatal("overwrote newer disabled config", v)
					}
				}
			})
		}
	}
}

func TestPlatformMemberCurrentStorageFailuresAndRestart(t *testing.T) {
	parallelFixtureTest(t)
	for _, provider := range []string{"lark", "gitlab"} {
		failures := []string{"reserve", "reserve_ambiguous", "complete", "corrupt"}
		if provider == "gitlab" {
			failures = []string{"complete"}
		}
		for _, fail := range failures {
			t.Run(provider+"/"+fail, func(t *testing.T) {
				m, _, full := memberNotificationToggleFixture(t, provider)
				req := currentMemberRequest(full)
				id := platformMemberToggleID(req)
				real := m.store.ops
				if fail == "corrupt" {
					if err := os.MkdirAll(m.store.dir+"/platform-member", 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(m.platformMemberPath(id), []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					m.mu.Lock()
					m.store.ops.rename = func(from, to string) error {
						if to == m.platformMemberPath(id) {
							var op platformMemberOperation
							data, _ := os.ReadFile(from)
							if json.Unmarshal(data, &op) == nil {
								if fail == "reserve_ambiguous" {
									if err := real.rename(from, to); err != nil {
										return err
									}
									return os.ErrPermission
								}
								if fail == "reserve" || op.Status == "completed" {
									return os.ErrPermission
								}
							}
						}
						return real.rename(from, to)
					}
					m.mu.Unlock()
				}
				if w := managerHTTP(m, "/v1/platforms/member/notifications", currentMemberWire(t, req)); w.Code == 200 {
					t.Fatal("storage failure reported success", w.Body.String())
				}
				m.mu.Lock()
				m.store.ops = real
				m.mu.Unlock()
				// A failed atomic save protects the live Store from further reads/writes.
				// Inspect through a fresh manager, as real recovery must do.
				if err := m.close(); err != nil {
					t.Fatal(err)
				}
				reopened := testManager(t, m.store.dir, nil)
				svc := eventLiveService(t, &eventHarness{m: reopened, room: roomSummary{RoomIdentity: req.RoomIdentity}})
				req.ServiceInfo, full.ServiceInfo = svc.info, svc.info
				v := gitlabMemberGet(t, reopened, full)
				if fail != "complete" {
					if v.NotificationVersion != 0 || !v.Baseline.IsZero() {
						t.Fatal("unreserved operation wrote settings", v)
					}
					if fail == "reserve_ambiguous" {
						op := applyCurrentMember(t, reopened, req)
						if op.Status != "unknown" || op.ID != id {
							t.Fatal("lost uncertain reservation", op)
						}
						if after := gitlabMemberGet(t, reopened, full); after.NotificationVersion != 0 {
							t.Fatal("executed uncertain reservation", after)
						}
					}
					return
				}
				if v.NotificationVersion != 1 {
					t.Fatal("expected actual write before completion failure", v)
				}
				replay := applyCurrentMember(t, reopened, req)
				if replay.Status != "unknown" || replay.ID != id || replay.Input.NotificationVersion != 0 {
					t.Fatal(replay)
				}
				if after := gitlabMemberGet(t, reopened, full); after.NotificationVersion != 1 || after.Baseline != v.Baseline {
					t.Fatal("unknown replayed after restart", after)
				}
				query := platformMemberRequest{ServiceInfo: svc.info, Caller: req.Caller, SourceSeq: req.SourceSeq}
				if w := managerHTTP(reopened, "/v1/platforms/member/status", query); w.Code != 200 || !strings.Contains(w.Body.String(), id) {
					t.Fatal(w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestPlatformMemberToggleIdentityScope(t *testing.T) {
	req := platformMemberRequest{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: strings.Repeat("a", 32)}, ServiceEpoch: "epoch"}, Caller: agentCaller("agent", "member"), SourceSeq: 123, Input: &platformMemberInput{PlatformID: strings.Repeat("b", 32), Enabled: boolPointer(true)}}
	id := platformMemberToggleID(req)
	for _, mutate := range []func(*platformMemberRequest){
		func(r *platformMemberRequest) { r.SourceSeq++ }, func(r *platformMemberRequest) { r.RoomID += "2" },
		func(r *platformMemberRequest) { r.RoomInstance = strings.Repeat("c", 32) }, func(r *platformMemberRequest) { r.Caller.ID += "2" },
		func(r *platformMemberRequest) { r.Caller.MembershipID += "2" }, func(r *platformMemberRequest) { r.Input.PlatformID = strings.Repeat("c", 32) },
	} {
		copy := req
		in := *req.Input
		copy.Input = &in
		mutate(&copy)
		if platformMemberToggleID(copy) == id {
			t.Fatal("different target/source shared ID")
		}
	}
	req.ServiceEpoch = "next epoch"
	req.Input.Enabled = boolPointer(false)
	req.Input.PlatformVersion = 999
	req.Input.NotificationVersion = 888
	if platformMemberToggleID(req) != id {
		t.Fatal("transient observation changed identity")
	}
}
