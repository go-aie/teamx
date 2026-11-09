package room

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

const timeReferencePrefix = "Current time (turn-start reference): "

func assertTimeSupply(t *testing.T, prompt, zone string, full bool) time.Time {
	t.Helper()
	if strings.Contains(prompt, "System time convention:") != full || strings.Count(prompt, timeReferencePrefix) != 1 {
		t.Fatal("incorrect time supply", prompt)
	}
	if full && !strings.Contains(prompt, "The current system time zone is "+zone+".") && !strings.Contains(prompt, "The system time zone is now "+zone+".") {
		t.Fatal("wrong time zone", prompt)
	}
	_, tail, _ := strings.Cut(prompt, timeReferencePrefix)
	line, _, _ := strings.Cut(tail, "\n")
	now, err := time.Parse(time.RFC3339, line)
	if err != nil {
		t.Fatal("invalid time reference", line, err)
	}
	location, err := loadTimeZone(zone)
	if err != nil || now.Format(time.RFC3339) != now.In(location).Format(time.RFC3339) {
		t.Fatal("reference uses a different zone", line, zone, err)
	}
	return now
}

func TestDSHTimeContextDatesAndDST(t *testing.T) {
	for _, tc := range []struct{ zone, utc, want string }{
		{"Asia/Shanghai", "2026-11-08T17:04:13Z", "2026-11-09T01:04:13+08:00"},
		{"UTC", "2026-11-08T17:04:13Z", "2026-11-08T17:04:13Z"},
		{"America/New_York", "2026-03-08T06:59:59Z", "2026-03-08T01:59:59-05:00"},
		{"America/New_York", "2026-03-08T07:00:00Z", "2026-03-08T03:00:00-04:00"},
		{"America/New_York", "2026-11-01T05:30:00Z", "2026-11-01T01:30:00-04:00"},
		{"America/New_York", "2026-11-01T06:30:00Z", "2026-11-01T01:30:00-05:00"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			location, _ := loadTimeZone(tc.zone)
			snapshot := settingsSnapshot{settingsView{tc.zone, 1}, location}
			now, _ := time.Parse(time.RFC3339, tc.utc)
			full := dshTimeContext(snapshot, now, "")
			assertTimeSupply(t, full, tc.zone, true)
			if !strings.HasPrefix(full, "System time convention: The current system time zone is "+tc.zone+".") || strings.Contains(full, "replaces the previous") {
				t.Fatal("initial supply refers to a previous convention", full)
			}
			changed := dshTimeContext(snapshot, now, "another-zone")
			assertTimeSupply(t, changed, tc.zone, true)
			if !strings.HasPrefix(changed, "System time convention: The system time zone is now "+tc.zone+". This convention replaces the previous system time zone convention.") {
				t.Fatal("zone change does not explicitly replace the previous convention", changed)
			}
			for _, clause := range []string{"Z means UTC", "interpret other explicit offsets as given", "today/yesterday", "do not copy UTC clock times as local times", "third-party text and images", "do not guess a missing time zone", "a long turn lacks an up-to-date time reference", "Use full dates"} {
				if !strings.Contains(full, clause) {
					t.Fatal("missing time contract", clause)
				}
			}
			if !strings.Contains(full, timeReferencePrefix+tc.want) {
				t.Fatal(full)
			}
			next := dshTimeContext(snapshot, now.Add(time.Second), tc.zone)
			if got := assertTimeSupply(t, next, tc.zone, false); !got.Equal(now.Add(time.Second)) || strings.Count(next, "\n") != 1 {
				t.Fatal("ordinary turn did not refresh its one-line reference", next)
			}
		})
	}
}

func TestDSHTimeSupplyLifecycleAndPolicyIndependence(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("one"))
	inboxRunning(t, h, 1, 1)
	settings := settingsFromFile(t, filepath.Join(t.TempDir(), "config.toml"), false)
	var prompts []string
	var opened []dsh.Options
	fail := false
	b := dshBackend{store: h.store, settings: settings, open: func(_ context.Context, opts dsh.Options) (dshComponent, error) {
		opened = append(opened, opts)
		return &adapterComponent{run: func(_ context.Context, prompt string) (dsh.TurnResult, error) {
			prompts = append(prompts, prompt)
			if fail {
				return dsh.TurnResult{}, errors.New("rejected prompt")
			}
			return dsh.TurnResult{}, nil
		}}, nil
	}}
	opts := adapterOptions(h.store, h.id)
	session, err := b.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	input := UnreadChanged{MemberIdentity: h.id}
	run := func(fullTime, fullPolicy bool) {
		t.Helper()
		before := time.Now().Truncate(time.Second)
		if err := session.Run(h.ctx, input); err != nil {
			t.Fatal(err)
		}
		prompt := prompts[len(prompts)-1]
		now := assertTimeSupply(t, prompt, settings.snapshot().TimeZone, fullTime)
		if now.Before(before) || now.After(time.Now()) {
			t.Fatal("stale current time", now)
		}
		_, supplied := suppliedPolicy(t, prompt)["effective"]
		if supplied != fullPolicy {
			t.Fatal("policy and time supply were coupled", prompt)
		}
	}
	run(true, true)
	run(false, false)
	if _, err := h.store.changePolicy(h.id, humanCaller, policyRule()); err != nil {
		t.Fatal(err)
	}
	run(false, true)
	if _, err := settings.save("UTC", 1); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 3 {
		t.Fatal("saving settings started a model turn")
	}
	run(true, false)
	run(false, false)
	fail = true
	if err := session.Run(h.ctx, input); err == nil || session.(*dshSession).timeZone != "" {
		t.Fatal("failed prompt retained supply marker", err)
	}
	fail = false
	run(true, true)
	// Preparation/cancellation failures also invalidate the time marker and do
	// not retry the model. Policy has its own existing supply semantics.
	canceled, cancel := context.WithCancel(h.ctx)
	cancel()
	count := len(prompts)
	if err := session.Run(canceled, input); !errors.Is(err, context.Canceled) || len(prompts) != count {
		t.Fatal("canceled turn reached model", err)
	}
	run(true, false)
	if err := session.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	b.recover = true
	session, err = b.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	run(true, true)
	if !reflect.DeepEqual(opened[0], opened[1]) {
		t.Fatal("time zone changed frozen options/recovery fingerprint")
	}
	if err := session.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	next, err := rotateAdapterForTest(b, opts, true)
	if err != nil {
		t.Fatal(err)
	}
	session, err = next.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(h.ctx)
	run(true, true)
	if strings.Contains(opened[2].Instructions, "System time convention:") || strings.Contains(opened[2].Instructions, timeReferencePrefix) {
		t.Fatal("time entered Session Instructions")
	}
}

func TestDSHTimeSupplySnapshotAcrossPreparationAndExecution(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	settings := settingsFromFile(t, filepath.Join(t.TempDir(), "config.toml"), false)
	var prompts []string
	prepare := true
	session := &dshSession{opts: adapterOptions(h.store, h.id), backend: dshBackend{store: h.store, settings: settings,
		prepare: func(context.Context, UnreadChanged, uint64, bool) (*dshMaterials, error) {
			if prepare {
				if _, err := settings.save("UTC", 1); err != nil {
					t.Fatal(err)
				}
			}
			return &dshMaterials{check: func(context.Context) error { return nil }}, nil
		}}, component: &adapterComponent{runPrompt: func(_ context.Context, blocks []dsh.ContentBlock) (dsh.TurnResult, error) {
		prompts = append(prompts, blocks[0].Text)
		if prepare {
			if _, err := settings.save("America/New_York", 2); err != nil {
				t.Fatal(err)
			}
		}
		return dsh.TurnResult{}, nil
	}}}
	input := UnreadChanged{MemberIdentity: h.id}
	if err := session.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	assertTimeSupply(t, prompts[0], "Asia/Shanghai", true)
	if session.timeZone != "Asia/Shanghai" {
		t.Fatal("marker records settings saved during execution")
	}
	prepare = false
	if err := session.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	assertTimeSupply(t, prompts[1], "America/New_York", true)
	if strings.Contains(prompts[1], "Asia/Shanghai") || strings.Contains(prompts[0], "America/New_York") {
		t.Fatal("mixed settings snapshots")
	}
}

func TestDSHTimeSupplyCountsAgainstMaterialBudget(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	called := false
	base := ""
	session := &dshSession{opts: adapterOptions(h.store, h.id), backend: dshBackend{store: h.store,
		prepare: func(context.Context, UnreadChanged, uint64, bool) (*dshMaterials, error) {
			return &dshMaterials{notes: []string{strings.Repeat("x", (1<<20)-len(base)-len(materialWarning))}, check: func(context.Context) error { return nil }}, nil
		}}, component: &adapterComponent{runPrompt: func(context.Context, []dsh.ContentBlock) (dsh.TurnResult, error) {
		called = true
		return dsh.TurnResult{}, nil
	}}}
	// Without the time context this payload fits exactly; the new first-turn
	// contract and reference must participate in the existing combined budget.
	policy, err := h.store.readAgentPolicy(h.id)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(policy)
	base = "Member policy context (JSON):\n" + string(body) + "\n\n" + dshNotification(UnreadChanged{})
	if err := session.Run(h.ctx, UnreadChanged{MemberIdentity: h.id}); err == nil || !strings.Contains(err.Error(), "budget") || called || session.timeZone != "" {
		t.Fatal("oversized prompt reached model or recorded supply", err, called)
	}
}

func TestDSHTimeSupplyPreparationFailures(t *testing.T) {
	for _, stage := range []string{"prepare", "final-check"} {
		t.Run(stage, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			failed := false
			calls := 0
			rejected := errors.New("material access revoked")
			session := &dshSession{opts: adapterOptions(h.store, h.id), backend: dshBackend{store: h.store,
				prepare: func(context.Context, UnreadChanged, uint64, bool) (*dshMaterials, error) {
					if failed && stage == "prepare" {
						return nil, rejected
					}
					return &dshMaterials{check: func(context.Context) error {
						if failed {
							return rejected
						}
						return nil
					}}, nil
				}}, component: &adapterComponent{runPrompt: func(_ context.Context, prompt []dsh.ContentBlock) (dsh.TurnResult, error) {
				calls++
				assertTimeSupply(t, prompt[0].Text, defaultTimeZone, true)
				return dsh.TurnResult{}, nil
			}}}
			input := UnreadChanged{MemberIdentity: h.id}
			if err := session.Run(h.ctx, input); err != nil {
				t.Fatal(err)
			}
			failed = true
			if err := session.Run(h.ctx, input); !errors.Is(err, rejected) || calls != 1 || session.timeZone != "" {
				t.Fatal("failed preparation submitted or retained time supply", err, calls)
			}
			failed = false
			if err := session.Run(h.ctx, input); err != nil || calls != 2 {
				t.Fatal("next normal turn did not supply time convention", err, calls)
			}
		})
	}
}
