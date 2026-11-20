package platform

import "slices"

// TypeDefinition describes implementations, never member permissions or runtime
// availability. An empty ToolType creates no platform tool entry.
type TypeDefinition struct {
	Provider             string   `json:"provider"`
	ToolType             string   `json:"tool_type,omitempty"`
	NotificationChannels []string `json:"notification_channels,omitempty"`
}

// Catalog is a value snapshot supplied by application assembly. The common
// platform contract knows no built-in provider list or implementation factories.
type Catalog struct{ types []TypeDefinition }

func NewCatalog(types []TypeDefinition) Catalog {
	return Catalog{types: cloneTypes(types)}
}

func cloneTypes(types []TypeDefinition) []TypeDefinition {
	out := slices.Clone(types)
	for i := range out {
		out[i].NotificationChannels = slices.Clone(out[i].NotificationChannels)
	}
	return out
}

func (c Catalog) Types() []TypeDefinition { return cloneTypes(c.types) }

func (c Catalog) LookupType(provider string) (TypeDefinition, bool) {
	for _, d := range c.types {
		if d.Provider == provider {
			d.NotificationChannels = slices.Clone(d.NotificationChannels)
			return d, true
		}
	}
	return TypeDefinition{}, false
}
