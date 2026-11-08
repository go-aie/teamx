package gitlab

import (
	"github.com/go-aie/teamx/internal/platform/notification"
	"time"
)

const (
	DefaultIntervalSeconds  = 30
	MinIntervalSeconds      = 3
	InactiveIntervalSeconds = 120
)

func normalizeInterval(seconds int) (int, error) {
	if seconds == 0 {
		seconds = DefaultIntervalSeconds
	}
	if seconds < MinIntervalSeconds || seconds > InactiveIntervalSeconds {
		return 0, notification.ErrRequest
	}
	return seconds, nil
}

// NotificationConfig is the user configuration for one Room's GitLab source.
// The concrete source configs below are derived runtime snapshots, not settings.
type NotificationConfig struct {
	BackfillSeconds int                 `json:"backfill_seconds,omitempty"`
	BackfillStart   time.Time           `json:"backfill_start,omitzero"`
	ConnectionID    string              `json:"connection_id"`
	Target          notification.Target `json:"target"`
	IntervalSeconds int                 `json:"interval_seconds"`
	Events          EventSettings       `json:"events"`
	Todos           TodoSettings        `json:"todos"`
}

type EventSettings struct {
	Enabled      bool         `json:"enabled"`
	Project      string       `json:"project"`
	Filter       EventsFilter `json:"filter"`
	ETagVerified bool         `json:"etag_verified,omitempty"`
}

type TodoSettings struct {
	Enabled bool        `json:"enabled"`
	Filter  TodosFilter `json:"filter"`
}

func (c NotificationConfig) EventConfig() EventsConfig {
	return EventsConfig{BackfillSeconds: c.BackfillSeconds, BackfillStart: c.BackfillStart, ConnectionID: c.ConnectionID, Target: c.Target, Enabled: c.Events.Enabled, Project: c.Events.Project, IntervalSeconds: c.IntervalSeconds, Filter: c.Events.Filter, ETagVerified: c.Events.ETagVerified}
}
func (c NotificationConfig) TodoConfig() TodosConfig {
	return TodosConfig{BackfillSeconds: c.BackfillSeconds, BackfillStart: c.BackfillStart, ConnectionID: c.ConnectionID, Target: c.Target, Enabled: c.Todos.Enabled, IntervalSeconds: c.IntervalSeconds, Filter: c.Todos.Filter}
}
func EventSettingsFrom(c EventsConfig) EventSettings {
	return EventSettings{Enabled: c.Enabled, Project: c.Project, Filter: c.Filter, ETagVerified: c.ETagVerified}
}
func TodoSettingsFrom(c TodosConfig) TodoSettings {
	return TodoSettings{Enabled: c.Enabled, Filter: c.Filter}
}

func NormalizeNotificationConfig(c NotificationConfig) (NotificationConfig, error) {
	interval, err := normalizeInterval(c.IntervalSeconds)
	if err != nil {
		return c, err
	}
	c.IntervalSeconds = interval
	start, err := notification.NormalizeBackfill(c.BackfillSeconds, c.BackfillStart)
	if err != nil {
		return c, err
	}
	c.BackfillStart = start
	if !c.Target.Inbox() {
		if !c.Target.Valid() {
			return c, notification.ErrRequest
		}
		c.Target = notification.Target{}
	}
	if (c.Events.Enabled || c.Todos.Enabled) && !validRestartID(c.ConnectionID) {
		return c, notification.ErrRequest
	}
	events, err := NormalizeEventsConfig(c.EventConfig())
	if err != nil {
		return c, err
	}
	todos, err := normalizeTodosConfig(c.TodoConfig(), c.ConnectionID != "" || c.Todos.Enabled)
	if err != nil {
		return c, err
	}
	c.Events, c.Todos = EventSettingsFrom(events), TodoSettingsFrom(todos)
	return c, nil
}
