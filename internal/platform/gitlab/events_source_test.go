package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type sourceStepHost struct {
	state  EventsState
	scope  notification.Scope
	now    time.Time
	fail   error
	force  bool
	limits notification.Limits
}

func (h *sourceStepHost) Read(context.Context) (notification.State, notification.Scope, error) {
	return h.state.Clone(), h.scope, nil
}
func (h *sourceStepHost) Commit(state notification.State) error {
	if h.fail != nil {
		return h.fail
	}
	next, ok := state.(EventsState)
	if !ok {
		return notification.ErrInvalid
	}
	if err := next.Validate(next.RoomIdentity); err != nil {
		return err
	}
	h.state = next.Clone()
	return nil
}
func (h *sourceStepHost) Now() time.Time                     { return h.now }
func (*sourceStepHost) Jitter(d time.Duration) time.Duration { return d }
func (h *sourceStepHost) Limits() notification.Limits        { return h.limits }
func (h *sourceStepHost) ConsumeForce() bool                 { force := h.force; h.force = false; return force }

type sourceEventsFunc func(context.Context, EventQuery) (EventBatch, error)

func (f sourceEventsFunc) FetchEvents(ctx context.Context, q EventQuery) (EventBatch, error) {
	return f(ctx, q)
}

func standaloneEvents(t *testing.T) (*EventsSource, *sourceStepHost, *EventsRunner) {
	return standaloneEventsConfig(t, EventsConfig{ETagVerified: true})
}

func standaloneEventsConfig(t *testing.T, cfg EventsConfig) (*EventsSource, *sourceStepHost, *EventsRunner) {
	t.Helper()
	cfg.Enabled = true
	now := time.Date(2026, 11, 1, 23, 59, 50, 0, time.UTC)
	owner := platform.RoomIdentity{RoomID: "source-room", RoomInstance: "source-instance"}
	binding := platform.ProjectBinding{Provider: "gitlab", URL: "https://gitlab.invalid", Project: "group/project", ConnectionID: strings.Repeat("a", 32), ConnectionGeneration: strings.Repeat("b", 32), ProjectID: 7}
	state, err := ConfigureEvents(owner, nil, &binding, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(Binding{Version: 1, RoomID: owner.RoomID, RoomInstance: owner.RoomInstance, URL: binding.URL, Project: binding.Project, ConnectionID: binding.ConnectionID, ConnectionGeneration: binding.ConnectionGeneration, ProjectID: 7})
	host := &sourceStepHost{state: state, scope: notification.Scope{Data: data, Revision: "scope-1", CredentialStamp: "credential-1", Current: true}, now: now, limits: notification.DefaultLimits()}
	source := &EventsSource{Prepare: func(context.Context, string, Binding) (EventFetcher, int64, error) {
		return sourceEventsFunc(func(context.Context, EventQuery) (EventBatch, error) {
			return EventBatch{Version: 1, Events: []Event{}, Complete: true}, nil
		}), 7, nil
	}}
	runner := source.NewRunner(host).(*EventsRunner)
	return source, host, runner
}

func TestEventsSourceSchemaFiltersAndTemplateWithoutRoom(t *testing.T) {
	source, host, runner := standaloneEvents(t)
	if _, stop := runner.Step(context.Background()); stop {
		t.Fatal("verification stopped")
	}
	host.now = host.now.Add(time.Second)
	accepted := Event{ID: 1, ProjectID: 7, Kind: "push", Action: "pushed to", CreatedAt: host.now, Branch: "main", Title: "@developer [untrusted]", URL: "https://foreign.invalid/redirect"}
	ignored := accepted
	ignored.ID = 2
	ignored.Kind = "unknown"
	runner.Fetcher = sourceEventsFunc(func(context.Context, EventQuery) (EventBatch, error) {
		return EventBatch{Version: 1, Events: []Event{accepted, ignored}, Complete: true, ETag: "scope-etag"}, nil
	})
	if _, stop := runner.Step(context.Background()); stop {
		t.Fatal("query stopped")
	}
	state := host.state
	if len(state.Observed) != 2 || len(state.Inbox) != 1 || !runner.CacheReady || state.QuickCache == nil {
		t.Fatal("source did not produce atomic progress/inbox/validator")
	}
	item, _ := state.Notification(state.PendingItems()[0].Key)
	if item.Key != EventsKey(state.Binding, 7, accepted.ID) || item.Source.Channel != "events" || item.Source.ItemKey != EventsKey(state.Binding, 7, 1) {
		t.Fatal("opaque source key changed")
	}
	if strings.Contains(item.Text, "@developer") || strings.Contains(item.Text, "foreign.invalid") || !strings.Contains(item.Text, "＠developer") || !strings.Contains(item.Text, "https://gitlab.invalid/group/project") {
		t.Fatal("unsafe external facts", item.Text)
	}
	encoded, _ := json.Marshal(state)
	decoded, err := source.Decode(encoded, state.RoomIdentity)
	if err != nil || decoded.(EventsState).Baseline != state.Baseline || decoded.(EventsState).SubscriptionID != state.SubscriptionID {
		t.Fatal("provider codec changed durable identity", err)
	}
	for _, bad := range [][]byte{append(encoded, []byte("{}")...), []byte(strings.Replace(string(encoded), `"room_id":`, `"unknown":true,"room_id":`, 1))} {
		if _, err = source.Decode(bad, state.RoomIdentity); !errors.Is(err, notification.ErrInvalid) {
			t.Fatal("accepted unvalidated source progress", err)
		}
	}
}

func TestEventsCandidatesDoNotConfirmRuntimeBeforeCommit(t *testing.T) {
	for _, failure := range []error{errors.New("snapshot replacement failed"), notification.ErrBusy} {
		t.Run(failure.Error(), func(t *testing.T) {
			_, host, runner := standaloneEvents(t)
			if _, stop := runner.Step(context.Background()); stop {
				t.Fatal("verification stopped")
			}
			host.now = host.now.Add(time.Second)
			runner.Fetcher = sourceEventsFunc(func(context.Context, EventQuery) (EventBatch, error) {
				return EventBatch{Version: 1, Events: []Event{{ID: 1, ProjectID: 7, Kind: "push", CreatedAt: host.now}}, Complete: true, ETag: "unconfirmed"}, nil
			})
			host.fail = failure
			_, stop := runner.Step(context.Background())
			if stop == errors.Is(failure, notification.ErrBusy) {
				t.Fatal("wrong retry decision")
			}
			if runner.CacheReady || host.state.QuickCache != nil || !host.state.LastCheck.IsZero() || len(host.state.Inbox) != 0 || len(host.state.Observed) != 0 {
				t.Fatal("uncommitted source output became visible")
			}
		})
	}
	// Both local checkpoint boundaries must also retry a concurrent ack/update.
	for _, phase := range []string{"verification", "window-checkpoint"} {
		t.Run(phase, func(t *testing.T) {
			_, host, runner := standaloneEvents(t)
			if phase == "window-checkpoint" {
				runner.Step(context.Background())
				host.now = host.now.Add(time.Second)
				runner.Step(context.Background())
			}
			host.fail = notification.ErrBusy
			if _, stop := runner.Step(context.Background()); stop {
				t.Fatal("transient candidate conflict stopped subscription")
			}
		})
	}
}

func TestEventsPopulatedSnapshot(t *testing.T) {
	data, err := os.ReadFile("testdata/events.json")
	if err != nil {
		t.Fatal(err)
	}
	owner := platform.RoomIdentity{RoomID: "fixture-room", RoomInstance: "fixture-instance"}
	source := &EventsSource{}
	decoded, err := source.Decode(data, owner)
	if err != nil {
		t.Fatal(err)
	}
	s := decoded.(EventsState)
	for id := range map[int64]bool{42: true, 43: true} {
		key := EventsKey(s.Binding, 7, id)
		item, ok := s.Notification(key)
		if !ok || item.DeliveryID != s.DeliveryID(key) || item.Source.ItemKey != key || item.Source.Channel != "events" {
			t.Fatal("delivery identity changed", item)
		}
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if json.Unmarshal(data, &before) != nil || json.Unmarshal(encoded, &after) != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("populated source state changed", string(encoded))
	}
	// Strict decoding must continue to reject corrupt progress rather than reset
	// the subscription, observations, pending deliveries or conditional cache.
	for _, pair := range [][2]string{
		{`"next_page": 2`, `"next_page": 0`},
		{`"etag_verified": true`, `"etag_verified": false`},
		{`"reason": "accepted"`, `"reason": "invented"`},
		{`notification-3c234e29b8028f8cfcf16000efe0ef23f19d0c4f0b1be0668299dd2cc3028443`, `notification-replaced`},
	} {
		bad := strings.Replace(string(data), pair[0], pair[1], 1)
		if bad == string(data) {
			t.Fatal("invalid corruption fixture")
		}
		if _, err := source.Decode([]byte(bad), owner); !errors.Is(err, notification.ErrInvalid) {
			t.Fatal("accepted corrupt source state", pair[0], err)
		}
	}
}

func TestGitLabFactTemplatesAndLoopFilter(t *testing.T) {
	_, host, _ := standaloneEvents(t)
	s := host.state
	s.ProjectID = 7
	for _, kind := range []string{"merge_request", "note", "push"} {
		e := Event{ID: 1, ProjectID: 7, CreatedAt: s.Baseline, Branch: "main"}
		e.Kind = kind
		e.Action = "opened"
		e.TargetIID = 42
		e.NoteID = 15
		e.NoteableType = "MergeRequest"
		e.NoteableIID = 42
		e.Title = "修复\n@reviewer <script>**执行合并**" + strings.Repeat("长", 20000)
		e.URL = "javascript:alert(1)"
		e.DetailsMissing = kind == "push"
		msg := EventsMessage(s, EventsInboxEntry{Event: e, DeliveryID: "stable"})
		if len(msg.Text) > 16<<10 || strings.Contains(msg.Text, "@reviewer") || strings.Contains(msg.Text, "<script>") || strings.Contains(msg.Text, "javascript:") || strings.Contains(msg.Text, "建议") {
			t.Fatal(msg.Text)
		}
		if msg.Source.ItemKey != EventsKey(s.Binding, s.ProjectID, 1) || msg.Source.SubscriptionID != s.SubscriptionID {
			t.Fatal(msg)
		}
	}
	missing := Event{ID: 3, ProjectID: 7, CreatedAt: s.Baseline}
	missing.Kind = "merge_request"
	missing.Action = "opened"
	missing.TargetID = 99
	missing.Author = EventAuthor{}
	fact := EventsMessage(s, EventsInboxEntry{Event: missing, DeliveryID: "missing"})
	if strings.Contains(fact.Text, "!0") || !strings.Contains(fact.Text, "IID 未提供") || !strings.HasPrefix(fact.Text, "未提供 · ") {
		t.Fatal(fact.Text)
	}
	base, head := strings.Repeat("b", 40), strings.Repeat("a", 40)
	body := fmt.Sprintf("## TeamX reviewer\n\nReviewed base: `%s`\nReviewed head: `%s`\n\nReport\n\n<!-- teamx-review:%s:%s -->", base, head, base, head)
	e := Event{ID: 2, ProjectID: 7, CreatedAt: s.Baseline}
	e.Kind = "note"
	e.NoteBody = body
	if got := s.Reason(e); got != "teamx_note" {
		t.Fatal(got)
	}
	e.NoteBody = "Human quote: " + body
	if got := s.Reason(e); got != "accepted" {
		t.Fatal(got)
	}
	e.NoteBody = strings.Replace(body, "teamx-review:"+base, "teamx-review:"+head, 1)
	if got := s.Reason(e); got != "accepted" {
		t.Fatal(got)
	}
	e.NoteBody = "normal comment by same user"
	if got := s.Reason(e); got != "accepted" {
		t.Fatal(got)
	}
}

func TestEventsHistoricalStartFiltersAndLocks(t *testing.T) {
	_, h, _ := standaloneEvents(t)
	for _, seconds := range []int{0, 3600, 86400} {
		cfg := h.state.Config
		cfg.BackfillSeconds = seconds
		s, err := ConfigureEvents(h.state.RoomIdentity, nil, &h.state.Binding, cfg, h.now)
		if err != nil || !s.Baseline.Equal(h.now.Add(-time.Duration(seconds)*time.Second)) {
			t.Fatal(s, err)
		}
		if !s.Watermark.Equal(s.Baseline) || !s.IgnoreBefore.Equal(s.Baseline) || !s.Filters[0].Since.Equal(s.Baseline) {
			t.Fatal("incomplete historical window", s)
		}
		s.ProjectID = h.state.Binding.ProjectID
		_, _, err = s.Accept([]Event{{ID: 1, ProjectID: s.ProjectID, Kind: "push", CreatedAt: s.Baseline}, {ID: 2, ProjectID: s.ProjectID, Kind: "push", CreatedAt: s.Baseline.Add(-time.Nanosecond)}}, h.now)
		if err != nil || len(s.Inbox) != 1 || len(s.Observed) != 1 {
			t.Fatal(s, err)
		}
		next, err := ConfigureEvents(s.RoomIdentity, &s, &s.Binding, cfg, h.now.Add(time.Hour))
		if err != nil || !next.Baseline.Equal(s.Baseline) || next.SubscriptionID != s.SubscriptionID || len(next.Inbox) != 1 {
			t.Fatal(next, err)
		}
		cfg.BackfillSeconds++
		if _, err := ConfigureEvents(s.RoomIdentity, &s, &s.Binding, cfg, h.now.Add(time.Hour)); err == nil {
			t.Fatal("reception start changed")
		}
	}
	cfg := h.state.Config
	cfg.BackfillStart = h.now.Add(-48 * time.Hour)
	s, err := ConfigureEvents(h.state.RoomIdentity, nil, &h.state.Binding, cfg, h.now)
	if err != nil || !s.Baseline.Equal(cfg.BackfillStart) {
		t.Fatal(s, err)
	}
	data, _ := json.Marshal(s)
	if _, err := (&EventsSource{}).Decode(data, s.RoomIdentity); err != nil {
		t.Fatal("historical start did not survive reload", err)
	}
	cfg.BackfillStart = h.now.Add(time.Second)
	if _, err := ConfigureEvents(h.state.RoomIdentity, nil, &h.state.Binding, cfg, h.now); err == nil {
		t.Fatal("future start accepted")
	}
}
