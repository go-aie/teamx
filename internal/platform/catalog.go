package platform

import "slices"

// TypeDefinition registers reusable implementations, never member permissions
// or runtime availability. An empty ToolType creates no platform tool entry.
type TypeDefinition struct {
	Provider             string   `json:"provider"`
	ToolType             string   `json:"tool_type,omitempty"`
	NotificationChannels []string `json:"notification_channels,omitempty"`
}

// Types is the static platform catalog. Provider adapters and executable paths
// remain in their composition layers; this package imports no implementations.
func Types() []TypeDefinition {
	return []TypeDefinition{
		{Provider: "gitlab", ToolType: "gitlab", NotificationChannels: []string{"events", "todos"}},
		{Provider: "lark", ToolType: "lark", NotificationChannels: []string{"messages"}},
		{Provider: "scion", ToolType: "scion"},
	}
}

func LookupType(provider string) (TypeDefinition, bool) {
	for _, d := range Types() {
		if d.Provider == provider {
			d.NotificationChannels = slices.Clone(d.NotificationChannels)
			return d, true
		}
	}
	return TypeDefinition{}, false
}
