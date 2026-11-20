package platform

import "testing"

func TestTypeCatalogDoesNotGrantOrInventCapabilities(t *testing.T) {
	catalog := NewCatalog([]TypeDefinition{{Provider: "gitlab", ToolType: "gitlab", NotificationChannels: []string{"events", "todos"}}, {Provider: "scion", ToolType: "scion"}})
	gitlab, ok := catalog.LookupType("gitlab")
	if !ok || gitlab.ToolType != "gitlab" || len(gitlab.NotificationChannels) != 2 {
		t.Fatal("GitLab implementation registration missing")
	}
	scion, ok := catalog.LookupType("scion")
	if !ok || scion.ToolType != "scion" || len(scion.NotificationChannels) != 0 {
		t.Fatal("tool-only platform acquired notifications")
	}
	if _, ok := catalog.LookupType("unknown"); ok {
		t.Fatal("unknown platform acquired an implementation")
	}
	gitlab.NotificationChannels[0] = "mutated"
	if next, _ := catalog.LookupType("gitlab"); next.NotificationChannels[0] != "events" {
		t.Fatal("caller changed static catalog")
	}
}
