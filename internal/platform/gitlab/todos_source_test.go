package gitlab

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func sourceTodoState(t *testing.T) TodosState {
	t.Helper()
	owner := platform.RoomIdentity{RoomID: "room", RoomInstance: "instance"}
	b := TodosBinding{PlatformID: strings.Repeat("a", 32), PlatformGeneration: strings.Repeat("b", 32), URL: "https://gitlab.example.com", UserID: 101, Username: "recipient", CredentialVersion: 1, CredentialFingerprint: strings.Repeat("c", 64)}
	s, err := ConfigureTodos(owner, nil, b, TodosConfig{Enabled: true, PlatformID: b.PlatformID}, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestTodosSourceCodecIsolationAndTemplate(t *testing.T) {
	s := sourceTodoState(t)
	todo := Todo{ID: 9, RecipientID: 101, ProjectID: 7, Project: "group/project", Action: "review_requested", Reason: "review_requested", Kind: "merge_request", TargetType: "MergeRequest", TargetID: 70, TargetIID: 42, Actor: &TodoActor{ID: 2, Username: "alice"}, TargetAuthor: &TodoActor{ID: 3, Username: "author"}, Title: "@developer [unsafe](javascript:run)\n# ignored", URL: "https://gitlab.example.com/group/project/-/merge_requests/42", CreatedAt: s.Baseline, State: "pending"}
	s.Accept([]Todo{todo}, s.Baseline.Add(time.Second))
	data, _ := json.Marshal(s)
	source := &TodosSource{}
	decoded, err := source.Decode(data, s.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := decoded.Notification(TodosKey(s.Binding, 9))
	if !ok || item.Source.Channel != "todos" || item.Source.ItemKey != "https://gitlab.example.com|101|9" || item.Source.ProjectID != 7 || !strings.HasPrefix(item.Text, "alice · 请求评审：") || strings.Contains(item.Text, "@developer") || strings.Contains(item.Text, "@recipient") || strings.Contains(item.Text, "[unsafe]") {
		t.Fatal(item)
	}
	if !strings.Contains(item.Text, "＠recipient") || !strings.Contains(item.Text, "MR !42") || !strings.Contains(item.Text, "alice") {
		t.Fatal(item.Text)
	}
	if _, err = source.Decode(data, platform.RoomIdentity{RoomID: "foreign", RoomInstance: "other"}); err == nil {
		t.Fatal("foreign owner loaded")
	}
	clone := s.Clone()
	entry := clone.Inbox[TodosKey(clone.Binding, 9)]
	entry.Todo.Actor.Username = "mutated"
	clone.Filters[0].Filter.Reasons[0] = "mutated"
	clone.Config.Filter.Types[0] = "mutated"
	delete(clone.Observed, TodosKey(clone.Binding, 9))
	if s.Inbox[TodosKey(s.Binding, 9)].Todo.Actor.Username != "alice" || s.Filters[0].Filter.Reasons[0] == "mutated" || s.Config.Filter.Types[0] == "mutated" || len(s.Observed) != 1 {
		t.Fatal("snapshot alias escaped")
	}
	key := TodosKey(s.Binding, 9)
	ack := s.Acknowledge(key).(TodosState)
	ack.Accept([]Todo{todo}, s.Baseline.Add(time.Minute))
	if len(ack.Inbox) != 0 {
		t.Fatal("acknowledged todo replayed")
	}
	if notification.DeliveryID(s.Metadata().Key, key) == notification.DeliveryID(notification.Key{Owner: s.RoomIdentity, Source: EventsSourceID, SubscriptionID: s.SubscriptionID}, key) {
		t.Fatal("cross-channel delivery identity collision")
	}
}
func TestTodosConfigCannotMutateAccountOrImportHistory(t *testing.T) {
	s := sourceTodoState(t)
	cfg := s.Config
	other := s.Binding
	other.UserID++
	if _, err := ConfigureTodos(s.RoomIdentity, &s, other, cfg, s.Baseline.Add(time.Minute)); err == nil {
		t.Fatal("changed account reused subscription")
	}
	for _, invalid := range []TodosConfig{
		{Enabled: true, PlatformID: s.Binding.PlatformID, Filter: TodosFilter{Types: []string{"push"}}},
		{Enabled: true, PlatformID: s.Binding.PlatformID, Filter: TodosFilter{Reasons: []string{"unknown"}}},
		{Enabled: true, PlatformID: s.Binding.PlatformID, IntervalSeconds: 121},
		{Enabled: true, PlatformID: s.Binding.PlatformID, IntervalSeconds: 2},
	} {
		if _, err := ConfigureTodos(s.RoomIdentity, &s, s.Binding, invalid, s.Baseline.Add(time.Minute)); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	cfg.Enabled = false
	disabled, err := ConfigureTodos(s.RoomIdentity, &s, s.Binding, cfg, s.Baseline.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	resumed, err := ConfigureTodos(s.RoomIdentity, &disabled, s.Binding, cfg, s.Baseline.Add(2*time.Minute))
	if err != nil || !resumed.Baseline.Equal(s.Baseline) || resumed.SubscriptionID != s.SubscriptionID {
		t.Fatal(resumed, err)
	}
	retired, err := RetireTodos(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ConfigureTodos(s.RoomIdentity, &retired, s.Binding, cfg, s.Baseline.Add(time.Minute)); err == nil {
		t.Fatal("retired identity resumed")
	}
}

func TestTodosExplicitEmptyFilterDoesNotMeanAll(t *testing.T) {
	s := sourceTodoState(t)
	cfg := s.Config
	cfg.Filter.Reasons = []string{}
	next, err := ConfigureTodos(s.RoomIdentity, &s, s.Binding, cfg, s.Baseline.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	todo := Todo{CreatedAt: s.Baseline.Add(2 * time.Second), Reason: "mentioned", Kind: "work_item"}
	if next.Reason(todo) != "reason" || len(next.Clone().Config.Filter.Reasons) != 0 {
		t.Fatal("empty reasons broadened subscription")
	}
	cfg.Filter.Reasons = nil
	cfg.Filter.Types = []string{}
	next, err = ConfigureTodos(s.RoomIdentity, &next, s.Binding, cfg, s.Baseline.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	todo.CreatedAt = s.Baseline.Add(4 * time.Second)
	if next.Reason(todo) != "type" {
		t.Fatal("empty types broadened subscription")
	}
}

func TestTodosHistoricalStartFiltersAndLocks(t *testing.T) {
	seed := sourceTodoState(t)
	now := seed.Baseline
	for _, seconds := range []int{0, 3600, 86400} {
		cfg := seed.Config
		cfg.BackfillSeconds = seconds
		s, err := ConfigureTodos(seed.RoomIdentity, nil, seed.Binding, cfg, now)
		if err != nil || !s.Baseline.Equal(now.Add(-time.Duration(seconds)*time.Second)) {
			t.Fatal(s, err)
		}
		atStart := Todo{ID: 1, RecipientID: 101, ProjectID: 7, Project: "group/project", Action: "review_requested", Reason: "review_requested", Kind: "merge_request", TargetType: "MergeRequest", TargetID: 70, TargetIID: 42, Actor: &TodoActor{ID: 2, Username: "alice"}, Title: "history", URL: "https://gitlab.example.com/group/project/-/merge_requests/42", CreatedAt: s.Baseline, State: "pending"}
		before := atStart
		before.ID = 2
		before.CreatedAt = s.Baseline.Add(-time.Nanosecond)
		s.Accept([]Todo{before, atStart}, now)
		if len(s.Inbox) != 1 || len(s.Observed) != 1 || s.Reason(atStart) != "accepted" {
			t.Fatal("history filter did not cover baseline", s)
		}
		next, err := ConfigureTodos(s.RoomIdentity, &s, s.Binding, cfg, now.Add(time.Hour))
		if err != nil || !next.Baseline.Equal(s.Baseline) || next.SubscriptionID != s.SubscriptionID || len(next.Inbox) != 1 {
			t.Fatal(next, err)
		}
		cfg.BackfillSeconds++
		if _, err := ConfigureTodos(s.RoomIdentity, &s, s.Binding, cfg, now.Add(time.Hour)); err == nil {
			t.Fatal("reception start changed")
		}
	}
	cfg := seed.Config
	cfg.BackfillStart = now.Add(-48 * time.Hour)
	s, err := ConfigureTodos(seed.RoomIdentity, nil, seed.Binding, cfg, now)
	if err != nil || !s.Baseline.Equal(cfg.BackfillStart) || !s.Filters[0].Since.Equal(s.Baseline) {
		t.Fatal(s, err)
	}
	data, _ := json.Marshal(s)
	if _, err := (&TodosSource{}).Decode(data, s.RoomIdentity); err != nil {
		t.Fatal("historical start did not survive reload", err)
	}
	cfg.BackfillStart = now.Add(time.Second)
	if _, err := ConfigureTodos(seed.RoomIdentity, nil, seed.Binding, cfg, now); err == nil {
		t.Fatal("future start accepted")
	}
}
