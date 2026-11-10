package gitlab

import (
	"reflect"
	"strings"
	"testing"
)

func TestNotificationMasterSwitchRetainsCategoryPreferences(t *testing.T) {
	cfg := DefaultNotificationConfig(strings.Repeat("a", 32))
	cfg.Events = EventSettings{Enabled: true, Project: "group/project", Filter: EventsFilter{Types: []string{"note"}}}
	cfg.Todos.Filter = TodosFilter{Reasons: []string{"mentioned"}, Types: []string{"merge_request"}}
	enabled, err := NormalizeNotificationConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	disabled, err := NormalizeNotificationConfig(cfg)
	if err != nil || disabled.EventConfig().Enabled || disabled.TodoConfig().Enabled {
		t.Fatal(disabled, err)
	}
	disabled.Enabled = true
	if !reflect.DeepEqual(enabled, disabled) {
		t.Fatal("master switch changed category settings")
	}
	defaults := DefaultNotificationConfig("other-platform")
	if !defaults.Enabled || defaults.Events.Enabled || !defaults.Todos.Enabled || defaults.PlatformID != "other-platform" {
		t.Fatal(defaults)
	}
}
