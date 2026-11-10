package room

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

func lifecycleReceive(t *testing.T, h *inboxHarness, key string, created time.Time) {
	t.Helper()
	msg := h.receive(key)
	msg.Item.CreatedAt = created
	inboxCall[InboxReceipt](t, h, msg)
}
func maintainTestInbox(t *testing.T, h *inboxHarness, now time.Time, skip bool, pending ...string) InboxStatus {
	t.Helper()
	p := map[string]bool{}
	for _, id := range pending {
		p[id] = true
	}
	return inboxCall[InboxStatus](t, h, MaintainInbox{MemberIdentity: h.id, Policy: notification.ExpiryPolicy{AfterDays: 3, SkipExpired: skip}, Now: now, Pending: p})
}
func TestInboxLifecycleSkipAndResumeMixedOrder(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	now := time.Now().UTC()
	old := now.Add(-4 * 24 * time.Hour)
	for _, x := range []struct {
		key string
		at  time.Time
	}{{"old", old}, {"fresh", now}, {"late-old", old}, {"fresh2", now}} {
		lifecycleReceive(t, h, x.key, x.at)
	}
	status := maintainTestInbox(t, h, now, true)
	if status.ExpiredThrough != 1 || status.ConsumedThrough != 0 || status.SkippedEntries != 2 || status.inputThrough() != 1 {
		t.Fatal(status)
	}
	h.reload(t, true)
	status = inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.ConsumedThrough != 0 || status.inputThrough() != 1 {
		t.Fatal(status)
	}
	// Disabling skips / extending the lifetime cannot resurrect a durable skip.
	status = maintainTestInbox(t, h, now, false)
	if status.inputThrough() != 1 {
		t.Fatal(status)
	}
	referenceRunning(t, h, 1, 1, 4)
	page := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	if len(page.Entries) != 2 || page.Entries[0].Seq != 2 || page.Entries[1].Seq != 4 {
		t.Fatal(page)
	}
	linkInboxPage(t, h, page)
	if s := confirmInboxPage(t, h, page); s.ConsumedThrough != 4 {
		t.Fatal(s)
	}
	settleReclaimTurn(t, h)
	// Every body eventually expires; skips never require fabricated followups.
	status = maintainTestInbox(t, h, now.Add(4*24*time.Hour), false)
	if status.ExpiredThrough != 4 || status.RetainedEntries != 0 {
		t.Fatal(status)
	}
	h.reload(t, true)
	if _, err := h.store.listFollowups(h.id, followupQuery{Limit: 25}); err != nil {
		t.Fatal(err)
	}
}
func TestInboxLifecycleKeepOldUntilConsumedAndAcknowledged(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	now := time.Now().UTC()
	lifecycleReceive(t, h, "old", now.Add(-4*24*time.Hour))
	if s := maintainTestInbox(t, h, now, false); s.ExpiredThrough != 0 || s.SkippedEntries != 0 {
		t.Fatal(s)
	}
	inboxRunning(t, h, 1, 1)
	page := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	linkInboxPage(t, h, page)
	confirmInboxPage(t, h, page)
	settleReclaimTurn(t, h)
	if s := maintainTestInbox(t, h, now, false, "delivery-old"); s.ExpiredThrough != 0 || s.ConsumedThrough != 1 {
		t.Fatal(s)
	}
	h.reload(t, true)
	if s := maintainTestInbox(t, h, now, false, "delivery-old"); s.ExpiredThrough != 0 {
		t.Fatal(s)
	}
	if s := maintainTestInbox(t, h, now, false); s.ExpiredThrough != 1 || s.RetainedEntries != 0 {
		t.Fatal(s)
	}
}
func TestInboxLifecycleActiveTurnBatchAndSweepBound(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	now := time.Now().UTC()
	lifecycleReceive(t, h, "old", now.Add(-4*24*time.Hour))
	inboxRunning(t, h, 1, 1)
	msg := MaintainInbox{MemberIdentity: h.id, Policy: notification.ExpiryPolicy{AfterDays: 3, SkipExpired: true}, Now: now}
	inboxError[InboxStatus](t, h, msg, ErrBusy)
	page := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	settleReclaimTurn(t, h)
	inboxError[InboxStatus](t, h, msg, ErrBusy)
	inboxRunning(t, h, 2, 1)
	page = inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 2})
	linkInboxPage(t, h, page)
	confirmInboxPage(t, h, page)
	settleReclaimTurn(t, h)
	if s := maintainTestInbox(t, h, now, true); s.ExpiredThrough != 1 {
		t.Fatal(s)
	}
	// Cleanup is bounded even when an entire backlog expires simultaneously.
	for i := 0; i < 130; i++ {
		lifecycleReceive(t, h, time.Unix(int64(i), 0).Format(time.RFC3339), now.Add(-5*24*time.Hour))
	}
	first := maintainTestInbox(t, h, now, true)
	if first.ExpiredThrough != 129 || first.RetainedEntries != 2 {
		t.Fatal(first)
	}
	last := maintainTestInbox(t, h, now, true)
	if last.ExpiredThrough != 131 || last.RetainedEntries != 0 || last.ConsumedThrough != 1 {
		t.Fatal(last)
	}
	h.reload(t, true)
}
func TestInboxLifecycleSkipCheckpointFailureAndReopen(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[unknown], func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			now := time.Now().UTC()
			lifecycleReceive(t, h, "old", now.Add(-4*24*time.Hour))
			save := h.store.ops.rename
			h.store.ops.rename = func(from, to string) error {
				if unknown {
					if err := save(from, to); err != nil {
						return err
					}
				}
				return errors.New("injected replacement failure")
			}
			_, err := h.addr.Call[InboxStatus](h.ctx, MaintainInbox{MemberIdentity: h.id, Policy: notification.ExpiryPolicy{AfterDays: 3, SkipExpired: true}, Now: now})
			if err == nil {
				t.Fatal("failure hidden")
			}
			if _, err := os.Stat(h.store.dir + "/inboxes/" + h.id.MembershipID + "/" + inboxEntryName(1)); err != nil {
				t.Fatal("body deleted before durable boundary", err)
			}
			h.store.ops.rename = save
			h.reload(t, true)
			s := maintainTestInbox(t, h, now, false)
			if unknown && s.inputThrough() != 1 {
				t.Fatal("durable skip lost", s)
			}
			if !unknown && s.inputThrough() != 0 {
				t.Fatal("failed skip confirmed", s)
			}
		})
	}
}
func TestInboxLifecycleStartupSettings(t *testing.T) {
	for _, test := range []struct {
		name, body string
		args       []string
		days       int
		skip       bool
	}{
		{"default", "", nil, 3, true},
		{"config", "[server]\ninbox_expire_after_days = 9\ninbox_skip_expired = false\n", nil, 9, false},
		{"override", "[server]\ninbox_expire_after_days = 9\ninbox_skip_expired = false\n", []string{"--inbox-expire-after-days=2", "--inbox-skip-expired=true"}, 2, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := loadStartupConfig(writeStartupConfig(t, t.TempDir(), test.body), true)
			if err != nil {
				t.Fatal(err)
			}
			f := flag.NewFlagSet("lifecycle", flag.ContinueOnError)
			f.SetOutput(io.Discard)
			days := f.Int("inbox-expire-after-days", 3, "")
			skip := f.Bool("inbox-skip-expired", true, "")
			if err := f.Parse(test.args); err != nil {
				t.Fatal(err)
			}
			explicit := map[string]bool{}
			f.Visit(func(v *flag.Flag) { explicit[v.Name] = true })
			if err := applyStartupSettings(f, explicit, cfg); err != nil {
				t.Fatal(err)
			}
			if *days != test.days || *skip != test.skip {
				t.Fatal(*days, *skip)
			}
		})
	}
	for _, days := range []int{-1, 0, 1000000} {
		if (notification.ExpiryPolicy{AfterDays: days}).Validate() == nil {
			t.Fatal("invalid days", days)
		}
	}
	p := notification.ExpiryPolicy{AfterDays: 7}
	now := time.Now().UTC()
	if !notification.Expired(now.Add(-7*24*time.Hour), p.Cutoff(now)) || notification.Expired(now.Add(-7*24*time.Hour+time.Nanosecond), p.Cutoff(now)) {
		t.Fatal("deadline boundary")
	}
	if !reflect.DeepEqual((notification.ExpiryPolicy{}).Resolved(), notification.ExpiryPolicy{AfterDays: 3, SkipExpired: true}) {
		t.Fatal("default")
	}
}

func TestInboxLifecycleSkippedImageCannotBecomeModelInput(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	now := time.Now().UTC()
	for _, n := range []int{1, 2, 3} {
		msg := h.receive(fmt.Sprint(n))
		msg.Item.Source = referenceTestSource(msg.Item.Key)
		msg.Item.Source.ProjectID = 123
		msg.Item.Source.Project = "org/repo"
		msg.Item.CreatedAt = now
		if n == 2 {
			msg.Item.CreatedAt = now.Add(-4 * 24 * time.Hour)
		}
		msg.Item.Text = fmt.Sprintf("image-body-%d ![x](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png)", n)
		inboxCall[InboxReceipt](t, h, msg)
	}
	maintainTestInbox(t, h, now, true)
	svc := referenceTestService(h)
	saveMaterialTurn(t, svc, h.id, 0, 3)
	materials, err := svc.prepareDSHMaterials(h.ctx, materialInput(h.id, 0, 3), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	text := materialText(materials.blocks("base"))
	if strings.Contains(text, "image-body-2") || strings.Contains(text, "Third-party material: Inbox #2") || !strings.Contains(text, "image-body-1") || !strings.Contains(text, "image-body-3") {
		t.Fatal("expiry filtering diverged for model images", text)
	}
}
