package scion

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type workitemsHost struct {
	s      State
	now    time.Time
	limits notification.Limits
	fail   bool
	force  bool
}

func (h *workitemsHost) Read(context.Context) (notification.State, notification.Scope, error) {
	b, _ := json.Marshal(QueryScope{Binding: h.s.Binding, Owner: h.s.RoomIdentity, SubscriptionID: h.s.SubscriptionID, Target: h.s.Config.Target, Profile: platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("c", 32), Executable: "/fixture/scn", CLIVersion: CLIVersion}})
	return h.s.Clone(), notification.Scope{Data: b}, nil
}
func (h *workitemsHost) Commit(s notification.State) error {
	if h.fail {
		return errors.New("disk full")
	}
	if e := s.Validate(h.s.RoomIdentity); e != nil {
		return e
	}
	h.s = s.(State).Clone()
	return nil
}
func (h *workitemsHost) Now() time.Time                       { return h.now }
func (h *workitemsHost) Jitter(d time.Duration) time.Duration { return d }
func (h *workitemsHost) Limits() notification.Limits          { return h.limits }
func (h *workitemsHost) ConsumeForce() bool                   { v := h.force; h.force = false; return v }
func workitemsFixture(t *testing.T) (*workitemsHost, *Source, *[]string) {
	t.Helper()
	b := Binding{PlatformID: strings.Repeat("a", 32), Generation: strings.Repeat("b", 32), Account: platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "scion", Instance: "https://scion.invalid"}, ID: platform.ExternalID{Namespace: "scion:subject", Value: "alice"}}, CredentialVersion: 1, Fingerprint: strings.Repeat("d", 64)}
	c := DefaultConfig(b.PlatformID)
	c.Repository = "Owner/repo"
	c.Target = notification.Target{Mode: "inbox", AgentID: "A", MembershipID: "member"}
	c.BackfillStart = time.Unix(1000, 0).UTC()
	s, e := Configure(platform.RoomIdentity{RoomID: "room", RoomInstance: "instance"}, nil, b, c, RepositoryEvidence{"Owner/repo", 100}, time.Unix(1010, 0))
	if e != nil {
		t.Fatal(e)
	}
	h := &workitemsHost{s: s, now: time.Unix(1010, 0).UTC(), limits: notification.DefaultLimits()}
	calls := []string{}
	source := &Source{Execute: func(_ context.Context, _ QueryScope, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[1] == "repo" {
			return []byte(`{"schema":"scion.cli.repo/v1","name":"Owner/repo","created_at_ms":100}`), nil
		}
		return issueList(t, []Issue{testIssue(1, 1000000), testIssue(2, 999999), testIssue(3, 1000000)}, false), nil
	}}
	return h, source, &calls
}
func testIssue(n, ms int64) Issue {
	return Issue{Schema: "scion.cli.issue/v1", Repo: "Owner/repo", Number: n, Title: "Issue", Author: "alice", State: "open", Created: ms, Updated: ms, Body: "hello"}
}
func issueList(t *testing.T, issues []Issue, truncated bool) []byte {
	t.Helper()
	b, e := json.Marshal(map[string]any{"schema": "scion.cli.issue-list/v1", "repo": "Owner/repo", "issues": issues, "truncated": truncated})
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestScionWorkitemsBoundariesRestartAndCodec(t *testing.T) {
	h, source, calls := workitemsFixture(t)
	r := source.NewRunner(h)
	r.Step(context.Background())
	r.Step(context.Background())
	if len(h.s.Inbox) != 2 || h.s.Discovery.LastFull.IsZero() || len(*calls) != 2 || !strings.Contains((*calls)[1], "issue ls --state open --limit 200") {
		t.Fatal(h.s, len(*calls))
	}
	for _, item := range h.s.PendingItems() {
		cap := NotificationCapabilities().Input
		q, e := cap.EncodeStoredInput(item)
		if e != nil {
			t.Fatal(e)
		}
		p, e := cap.Codec.Decode(1, q.Payload)
		if e != nil || !strings.Contains(p.Label, "Issue #") {
			t.Fatal(p, e)
		}
		rec := inputstream.StoredRecord{Schema: 1, Channel: q.Channel, CreatedAt: q.CreatedAt, DeliveryID: q.DeliveryID, Payload: q.Payload, DeliveryDigest: q.DeliveryDigest}
		round, e := cap.DecodeStoredInput(rec)
		if e != nil || !reflect.DeepEqual(item, round) {
			t.Fatal(e)
		}
		rec.DeliveryID = "tampered"
		if _, e = cap.DecodeStoredInput(rec); e == nil {
			t.Fatal("tampered delivery accepted")
		}
	}
	before := h.s.Clone()
	b, _ := json.Marshal(h.s)
	decoded, e := source.Decode(b, h.s.RoomIdentity)
	if e != nil {
		t.Fatal(e)
	}
	h.s = decoded.(State)
	h.now = h.now.Add(time.Minute)
	r = source.NewRunner(h)
	r.Step(context.Background())
	r.Step(context.Background())
	if len(h.s.Observed) != len(before.Observed) || h.s.Metrics.NewItems != before.Metrics.NewItems {
		t.Fatal("restart/reopen replayed creation")
	}
	h.fail = true
	h.now = h.now.Add(time.Minute)
	unchanged := h.s.Clone()
	r = source.NewRunner(h)
	r.Step(context.Background())
	if !reflect.DeepEqual(unchanged, h.s) {
		t.Fatal("save failure advanced progress")
	}
	h.fail = false
	cfg := h.s.Config
	cfg.Enabled = false
	paused, e := Configure(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.s.Repository, h.now)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Enabled = true
	resumed, e := Configure(h.s.RoomIdentity, &paused, h.s.Binding, cfg, h.s.Repository, h.now)
	if e != nil || resumed.SubscriptionID != before.SubscriptionID || !resumed.Baseline.Equal(before.Baseline) {
		t.Fatal("pause lost identity", e)
	}
	cfg.Repository = "Owner/other"
	if _, e = Configure(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.s.Repository, h.now); e == nil {
		t.Fatal("range mutation accepted")
	}
}
func TestScionWorkitemsTruncationAndCapacity(t *testing.T) {
	h, source, _ := workitemsFixture(t)
	run := source.Execute
	source.Execute = func(ctx context.Context, q QueryScope, args ...string) ([]byte, error) {
		if args[1] == "repo" {
			return run(ctx, q, args...)
		}
		return issueList(t, []Issue{testIssue(1, 1000000)}, true), nil
	}
	r := source.NewRunner(h)
	for range 10 {
		r.Step(context.Background())
	}
	if !h.s.Discovery.LastFull.IsZero() || !h.s.Discovery.Truncated || h.s.Discovery.Limit != MaxDiscoveryLimit || h.s.Status != "capacity" || len(h.s.Inbox) != 1 {
		t.Fatal(h.s.Discovery, h.s.Status)
	}
	h, source, _ = workitemsFixture(t)
	h.limits.Inbox = 1
	r = source.NewRunner(h)
	r.Step(context.Background())
	r.Step(context.Background())
	if len(h.s.Inbox) != 1 || !h.s.Discovery.LastFull.IsZero() || h.s.Status != "capacity" {
		t.Fatal("capacity advanced full scan")
	}
	for k := range h.s.Inbox {
		h.s = h.s.Acknowledge(k).(State)
	}
	r.Step(context.Background())
	if len(h.s.Inbox) != 1 || len(h.s.Observed) != 2 {
		t.Fatal("capacity relief lost key")
	}
	item := h.s.PendingItems()[0]
	h.s = h.s.Expire(item.CreatedAt).(State)
	if len(h.s.Inbox) != 0 {
		t.Fatal("expiry failed")
	}
	if _, ok := h.s.SourceTerminal(item); !ok {
		t.Fatal("expiry terminal proof missing")
	}
}
func TestScionWorkitemsRejectsIncompleteAndChangedIdentity(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "closed", "identity", "permission"} {
		t.Run(mode, func(t *testing.T) {
			h, source, _ := workitemsFixture(t)
			run := source.Execute
			source.Execute = func(ctx context.Context, q QueryScope, args ...string) ([]byte, error) {
				if args[1] == "repo" {
					if mode == "identity" {
						return []byte(`{"schema":"scion.cli.repo/v1","name":"Owner/repo","created_at_ms":200}`), nil
					}
					return run(ctx, q, args...)
				}
				switch mode {
				case "missing":
					return []byte(`{"schema":"scion.cli.issue-list/v1","repo":"Owner/repo"}`), nil
				case "duplicate":
					return issueList(t, []Issue{testIssue(1, 1000000), testIssue(1, 1000000)}, false), nil
				case "closed":
					i := testIssue(1, 1000000)
					i.State = "closed"
					return issueList(t, []Issue{i}, false), nil
				case "permission":
					return nil, errors.New("native exit 5")
				}
				return nil, nil
			}
			r := source.NewRunner(h)
			r.Step(context.Background())
			r.Step(context.Background())
			if len(h.s.Inbox) != 0 || !h.s.Discovery.LastFull.IsZero() || h.s.ErrorCode == "" {
				t.Fatal(h.s)
			}
			if mode == "permission" && h.s.Status == "credentials_invalid" {
				t.Fatal("business denial misclassified")
			}
		})
	}
}
