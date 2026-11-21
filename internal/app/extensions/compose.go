// Package extensions bridges public declarations to the single application catalog.
package extensions

import (
	"fmt"

	"github.com/go-aie/teamx/internal/app/platforms"
	internalplatform "github.com/go-aie/teamx/internal/platform"
	internalnotification "github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
	"github.com/go-aie/teamx/platform"
	"github.com/go-aie/teamx/platform/notification"
)

// Extension is a pure declaration. Full built-in platform contributions remain
// opaque; external authors declare only tools or notification contracts they use.
type Extension struct {
	Name          string
	Tools         []platform.Tool
	Notifications []notification.Provider
	platforms     []platforms.Factory
}

// Composition is an immutable checked selection. It owns no runtime resources.
type Composition struct {
	catalog *platforms.Catalog
	names   []string
}

// Platform is used by built-in adapters without exposing their internal
// notification, image or action contracts in the public author API.
func Platform(name string, factory platforms.Factory) Extension {
	return Extension{Name: name, platforms: []platforms.Factory{factory}}
}

func Compose(extensions ...Extension) (*Composition, error) {
	names := make(map[string]bool)
	var factories []platforms.Factory
	var tools []platforms.Tool
	var selected []string
	for _, e := range extensions {
		if !tool.ValidName(e.Name) || names[e.Name] {
			return nil, fmt.Errorf("invalid or duplicate extension: %q", e.Name)
		}
		names[e.Name] = true
		selected = append(selected, e.Name)
		factories = append(factories, e.platforms...)
		for _, provider := range e.Notifications {
			if len(provider.Sources) == 0 {
				return nil, fmt.Errorf("%s: notification sources missing", provider.Provider)
			}
			d := platforms.Definition{Type: internalplatform.TypeDefinition{Provider: provider.Provider},
				Notifications: internalnotification.Capabilities{Input: provider.Input, Project: provider.Project}}
			for _, source := range provider.Sources {
				if source.Bind == nil {
					return nil, fmt.Errorf("%s: notification runtime binding missing", provider.Provider)
				}
				d.Type.NotificationChannels = append(d.Type.NotificationChannels, source.ID.Channel)
				d.Sources = append(d.Sources, platforms.Source{ID: source.ID, New: source.New, Bind: source.Bind})
			}
			factories = append(factories, func() platforms.Definition { return d })
		}
		for _, t := range e.Tools {
			tools = append(tools, platforms.Tool{Executable: t.Executable, Definition: tool.Definition{
				Name: t.Name, Description: t.Description, Version: t.Version,
			}})
		}
	}
	catalog, err := platforms.NewCatalogWithTools(tools, factories...)
	if err != nil {
		return nil, err
	}
	return &Composition{catalog: catalog, names: selected}, nil
}

func Names(c *Composition) []string { return append([]string{}, c.names...) }

// Catalog is deliberately an internal free function, not a public method on
// Composition: external authors never need to name internal platform types.
func Catalog(c *Composition) (*platforms.Catalog, error) {
	if c == nil || c.catalog == nil {
		return nil, fmt.Errorf("extension composition missing; use extension.Compose")
	}
	return c.catalog, nil
}
