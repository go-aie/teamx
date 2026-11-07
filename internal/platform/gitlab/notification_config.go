package gitlab

import "github.com/go-aie/teamx/internal/platform/notification"

// NotificationConfig is the user configuration for one Room's GitLab source.
// The concrete source configs below are derived runtime snapshots, not settings.
type NotificationConfig struct {
	ConnectionID string              `json:"connection_id"`
	Target       notification.Target `json:"target"`
	Events       EventSettings       `json:"events"`
	Todos        TodoSettings        `json:"todos"`
}

type EventSettings struct {
	Enabled         bool         `json:"enabled"`
	Project         string       `json:"project"`
	IntervalSeconds int          `json:"interval_seconds"`
	Filter          EventsFilter `json:"filter"`
	ETagVerified    bool         `json:"etag_verified,omitempty"`
}

type TodoSettings struct {
	Enabled                 bool        `json:"enabled"`
	ProjectScope            string      `json:"project_scope"`
	IntervalSeconds         int         `json:"interval_seconds"`
	InactiveIntervalSeconds int         `json:"inactive_interval_seconds"`
	Filter                  TodosFilter `json:"filter"`
}

func (c NotificationConfig) EventConfig() EventsConfig {
	return EventsConfig{ConnectionID: c.ConnectionID, Target: c.Target, Enabled: c.Events.Enabled, Project: c.Events.Project, IntervalSeconds: c.Events.IntervalSeconds, Filter: c.Events.Filter, ETagVerified: c.Events.ETagVerified}
}
func (c NotificationConfig) TodoConfig() TodosConfig {
	return TodosConfig{ConnectionID: c.ConnectionID, Target: c.Target, Enabled: c.Todos.Enabled, ProjectScope: c.Todos.ProjectScope, IntervalSeconds: c.Todos.IntervalSeconds, InactiveIntervalSeconds: c.Todos.InactiveIntervalSeconds, Filter: c.Todos.Filter}
}
func EventSettingsFrom(c EventsConfig) EventSettings {
	return EventSettings{c.Enabled, c.Project, c.IntervalSeconds, c.Filter, c.ETagVerified}
}
func TodoSettingsFrom(c TodosConfig) TodoSettings {
	return TodoSettings{c.Enabled, c.ProjectScope, c.IntervalSeconds, c.InactiveIntervalSeconds, c.Filter}
}

func NormalizeNotificationConfig(c NotificationConfig) (NotificationConfig, error) {
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
