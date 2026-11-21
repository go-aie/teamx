package platforms

import (
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/go-aie/teamx/internal/app/platformtools"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Connection describes configuration checks. Account verification and credentials
// are bound by the service; none of these functions run while building a catalog.
type Connection struct {
	LegacyIdentity func(string, platform.Validation) (platform.AccountIdentity, bool)
	Normalize      func(string, string) (string, string, error)
	ValidProfile   func(string, platform.CLIProfile) bool
}

type Tool struct {
	Bind                            platformtools.Bind
	Definition                      tool.Definition
	Executable, OverrideEnvironment string
}

// Source describes a registered notification implementation. New constructs an
// unbound source without I/O. Bind supplies source-specific admission/recovery;
// the common host retains Room/member gates, persistence and lifecycle drain.
type Source struct {
	ID   notification.SourceID
	New  func() notification.Source
	Bind notification.Bind
}

// Definition is produced by an explicit, pure factory. The declaration and
// optional implementations must agree. Factories and source binders are pure;
// codec implementations are immutable. No capability grants execution access.
type Definition struct {
	Type          platform.TypeDefinition
	Connection    *Connection
	Tool          *Tool
	Sources       []Source
	Notifications notification.Capabilities
	References    *platform.ReferenceCapabilities
}

type Factory func() Definition

// Catalog owns a checked static snapshot. Returned collections are copies, so
// consumers cannot turn a list query into registration or permission changes.
type Catalog struct {
	types       platform.Catalog
	definitions []Definition
	tools       []Tool // independent accountless contributions
}

func NewCatalog(factories ...Factory) (*Catalog, error) {
	return NewCatalogWithTools(nil, factories...)
}

// NewCatalogWithTools checks platform and independent tools in one namespace.
// Standalone tools have no Provider, account environment or built-in dispatcher.
func NewCatalogWithTools(independent []Tool, factories ...Factory) (*Catalog, error) {
	c := &Catalog{}
	providers, tools := map[string]bool{}, map[string]bool{}
	types := []platform.TypeDefinition{}
	for _, factory := range factories {
		if factory == nil {
			return nil, fmt.Errorf("platform factory missing")
		}
		d := factory()
		p := d.Type.Provider
		if !tool.ValidName(p) || providers[p] {
			return nil, fmt.Errorf("invalid or duplicate platform provider: %q", p)
		}
		providers[p] = true
		if d.Connection != nil && d.Connection.Normalize == nil {
			return nil, fmt.Errorf("%s: connection normalizer missing", p)
		}
		if (d.Type.ToolType != "") != (d.Tool != nil) {
			return nil, fmt.Errorf("%s: tool declaration mismatch", p)
		}
		if d.Tool != nil {
			t := d.Tool
			if !tool.ValidName(t.Definition.Name) || t.Definition.Name != d.Type.ToolType || t.Definition.Provider != p || tools[t.Definition.Name] || t.Definition.Executable != "" || t.Definition.InternalOnly == (t.Executable != "") || t.Definition.InternalOnly != (t.Bind != nil) {
				return nil, fmt.Errorf("%s: invalid or duplicate tool implementation", p)
			}
			tools[t.Definition.Name] = true
		}
		channels := map[string]bool{}
		for _, channel := range d.Type.NotificationChannels {
			if !tool.ValidName(channel) || channels[channel] {
				return nil, fmt.Errorf("%s: invalid or duplicate notification channel", p)
			}
			channels[channel] = true
		}
		registered := map[string]bool{}
		for _, source := range d.Sources {
			if source.ID.Provider != p || !channels[source.ID.Channel] || registered[source.ID.Channel] || source.New == nil {
				return nil, fmt.Errorf("%s: notification declaration mismatch", p)
			}
			impl := source.New()
			if missingImplementation(impl) || impl.ID() != source.ID || impl.Schema() <= 0 {
				return nil, fmt.Errorf("%s: notification implementation missing or mismatched", p)
			}
			registered[source.ID.Channel] = true
		}
		if len(channels) != len(registered) {
			return nil, fmt.Errorf("%s: notification implementation missing", p)
		}
		n := d.Notifications
		if len(channels) == 0 && (n.Input != nil || n.Admission != nil || n.Project != nil || n.MatchAction != nil) {
			return nil, fmt.Errorf("%s: notification capabilities without source", p)
		}
		if n.Input != nil {
			if missingImplementation(n.Input.Codec) || n.Input.EncodeStoredInput == nil || n.Input.DecodeStoredInput == nil {
				return nil, fmt.Errorf("%s: incomplete input codec", p)
			}
			desc := n.Input.Codec.Descriptor()
			if desc.Provider != p || desc.Codec == "" || desc.Schema <= 0 || desc.GroupingVersion <= 0 {
				return nil, fmt.Errorf("%s: invalid input codec descriptor", p)
			}
			if len(desc.Channels) > 0 && !sameChannels(desc.Channels, d.Type.NotificationChannels) {
				return nil, fmt.Errorf("%s: input codec channel mismatch", p)
			}
		}
		if n.Admission != nil && (n.Admission.EvidencePath == nil || n.Admission.Inspect == nil) {
			return nil, fmt.Errorf("%s: incomplete notification admission", p)
		}
		if d.References != nil {
			if d.References.Resolve == nil && d.References.Read == nil && len(d.References.Actions) == 0 {
				return nil, fmt.Errorf("%s: reference implementation missing", p)
			}
			for operation, action := range d.References.Actions {
				if operation == "" || action.Spec.Operation != operation || action.Inspect == nil || action.Execute == nil {
					return nil, fmt.Errorf("%s: invalid reference action", p)
				}
			}
		}
		checked := cloneDefinition(d)
		c.definitions = append(c.definitions, checked)
		types = append(types, checked.Type)
	}
	for _, t := range independent {
		d := t.Definition
		if !tool.ValidName(d.Name) || tools[d.Name] || t.Executable == "" || d.Executable != "" || d.Provider != "" || d.InternalOnly || t.Bind != nil || len(d.Environment) != 0 {
			return nil, fmt.Errorf("invalid or duplicate independent tool: %q", d.Name)
		}
		tools[d.Name] = true
		t.Definition.Dependencies = slices.Clone(d.Dependencies)
		c.tools = append(c.tools, t)
	}
	c.types = platform.NewCatalog(types)
	return c, nil
}

func sameChannels(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func cloneDefinition(d Definition) Definition {
	d.Type.NotificationChannels = slices.Clone(d.Type.NotificationChannels)
	d.Sources = slices.Clone(d.Sources)
	if d.Connection != nil {
		v := *d.Connection
		d.Connection = &v
	}
	if d.Tool != nil {
		v := *d.Tool
		v.Definition.Environment = maps.Clone(v.Definition.Environment)
		v.Definition.Dependencies = slices.Clone(v.Definition.Dependencies)
		d.Tool = &v
	}
	d.Notifications = cloneNotifications(d.Notifications)
	if d.References != nil {
		v := cloneReferences(*d.References)
		d.References = &v
	}
	return d
}

func (c *Catalog) Types() platform.Catalog { return c.types }
func (c *Catalog) Definitions() []Definition {
	out := make([]Definition, 0, len(c.definitions))
	for _, d := range c.definitions {
		out = append(out, cloneDefinition(d))
	}
	return out
}
func (c *Catalog) NotificationCapabilities(provider string) notification.Capabilities {
	for _, d := range c.definitions {
		if d.Type.Provider == provider {
			return cloneNotifications(d.Notifications)
		}
	}
	return notification.Capabilities{}
}
func (c *Catalog) ReferenceCapabilities(provider string) (platform.ReferenceCapabilities, bool) {
	for _, d := range c.definitions {
		if d.Type.Provider == provider && d.References != nil {
			return cloneReferences(*d.References), true
		}
	}
	return platform.ReferenceCapabilities{}, false
}

// Tools binds executable paths only when the outer composition requests them.
// Static catalog construction never reads the environment or filesystem.
func (c *Catalog) Tools(resolve func(name, overrideEnvironment string) string) []tool.Definition {
	out := []tool.Definition{}
	for _, d := range c.Definitions() {
		if d.Tool == nil {
			continue
		}
		v := d.Tool.Definition
		if d.Tool.Executable != "" {
			v.Executable = resolve(d.Tool.Executable, d.Tool.OverrideEnvironment)
		}
		out = append(out, v)
	}
	for _, t := range c.tools {
		v := t.Definition
		v.Dependencies = slices.Clone(v.Dependencies)
		v.Executable = resolve(t.Executable, t.OverrideEnvironment)
		out = append(out, v)
	}
	return out
}

func missingImplementation(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}

func cloneNotifications(n notification.Capabilities) notification.Capabilities {
	if n.Input != nil {
		v := *n.Input
		n.Input = &v
	}
	if n.Admission != nil {
		v := *n.Admission
		n.Admission = &v
	}
	return n
}

func cloneReferences(v platform.ReferenceCapabilities) platform.ReferenceCapabilities {
	v.CredentialKinds = slices.Clone(v.CredentialKinds)
	v.Actions = maps.Clone(v.Actions)
	for op, action := range v.Actions {
		action.Spec.ObjectTypes = slices.Clone(action.Spec.ObjectTypes)
		action.Spec.InputSchema = slices.Clone(action.Spec.InputSchema)
		v.Actions[op] = action
	}
	return v
}

// ToolRuntime returns only the explicitly selected built-in application adapter.
func (c *Catalog) ToolRuntime(provider string) platformtools.Bind {
	for _, d := range c.definitions {
		if d.Type.Provider == provider && d.Tool != nil {
			return d.Tool.Bind
		}
	}
	return nil
}
