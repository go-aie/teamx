package platforms_test

import (
	"testing"

	gitlabplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/gitlab"
	larkplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/lark"
	scionplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/scion"
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/app/platformtools"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func notificationOnly() platforms.Definition {
	return platforms.Definition{Type: platform.TypeDefinition{Provider: "gitlab", NotificationChannels: []string{"events"}}, Sources: []platforms.Source{{ID: gitlab.EventsSourceID, New: func() notification.Source { return &gitlab.EventsSource{} }}}}
}
func toolOnly() platforms.Definition { return scionplatform.Definition() }

func TestOptionalCapabilitiesRemainIndependent(t *testing.T) {
	catalog, err := platforms.NewCatalog(notificationOnly, toolOnly)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := catalog.Types().LookupType("gitlab")
	if !ok || d.ToolType != "" || len(d.NotificationChannels) != 1 {
		t.Fatal(d)
	}
	if catalog.NotificationCapabilities("gitlab").Input != nil {
		t.Fatal("source implicitly acquired a codec")
	}
	if _, ok := catalog.ReferenceCapabilities("gitlab"); ok {
		t.Fatal("source implicitly acquired references")
	}
	tools := catalog.Tools(func(name, _ string) string { return name })
	if len(tools) != 1 || tools[0].Provider != "scion" {
		t.Fatal(tools)
	}
}

func TestCatalogRejectsInconsistentRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*platforms.Definition)
	}{
		{"missing tool", func(d *platforms.Definition) { d.Tool = nil }},
		{"undeclared tool", func(d *platforms.Definition) { d.Type.ToolType = "" }},
		{"wrong tool provider", func(d *platforms.Definition) { d.Tool.Definition.Provider = "other" }},
		{"wrong tool type", func(d *platforms.Definition) { d.Tool.Definition.Name = "other" }},
		{"missing executable", func(d *platforms.Definition) { d.Tool.Executable = "" }},
		{"missing source", func(d *platforms.Definition) { d.Sources = d.Sources[:1] }},
		{"undeclared source", func(d *platforms.Definition) { d.Type.NotificationChannels = []string{"events"} }},
		{"duplicate channel", func(d *platforms.Definition) {
			d.Type.NotificationChannels = append(d.Type.NotificationChannels, "events")
		}},
		{"duplicate source", func(d *platforms.Definition) { d.Sources = append(d.Sources, d.Sources[0]) }},
		{"missing source factory", func(d *platforms.Definition) { d.Sources[0].New = nil }},
		{"nil source", func(d *platforms.Definition) { d.Sources[0].New = func() notification.Source { return nil } }},
		{"typed nil source", func(d *platforms.Definition) {
			d.Sources[0].New = func() notification.Source { return (*gitlab.EventsSource)(nil) }
		}},
		{"wrong source implementation", func(d *platforms.Definition) {
			d.Sources[0].New = func() notification.Source { return &gitlab.TodosSource{} }
		}},
		{"incomplete codec", func(d *platforms.Definition) { d.Notifications.Input.EncodeStoredInput = nil }},
		{"wrong codec provider", func(d *platforms.Definition) { d.Notifications.Input = larkplatform.Definition().Notifications.Input }},
		{"incomplete admission", func(d *platforms.Definition) { d.Notifications.Admission = &notification.AdmissionCapability{} }},
		{"empty references", func(d *platforms.Definition) { d.References = &platform.ReferenceCapabilities{} }},
		{"missing input codec", func(d *platforms.Definition) { d.Notifications.Input.Codec = nil }},
		{"missing input encoder", func(d *platforms.Definition) { d.Notifications.Input.EncodeStoredInput = nil }},
		{"missing input decoder", func(d *platforms.Definition) { d.Notifications.Input.DecodeStoredInput = nil }},
		{"wrong source implementation", func(d *platforms.Definition) {
			d.Sources[0].New = func() notification.Source { return &gitlab.TodosSource{} }
		}},
		{"missing normalizer", func(d *platforms.Definition) { d.Connection.Normalize = nil }},
		{"inconsistent action", func(d *platforms.Definition) {
			a := d.References.Actions[platform.OperationObjectComment]
			a.Spec.Operation = platform.OperationMessageSend
			d.References.Actions[platform.OperationObjectComment] = a
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := platforms.NewCatalog(func() platforms.Definition { d := gitlabplatform.Definition(); tc.change(&d); return d })
			if err == nil {
				t.Fatal("inconsistent registration accepted")
			}
		})
	}
	for _, factories := range [][]platforms.Factory{{nil}, {gitlabplatform.Definition, gitlabplatform.Definition}, {func() platforms.Definition { return platforms.Definition{} }}, {toolOnly, func() platforms.Definition {
		d := toolOnly()
		d.Type.Provider = "other"
		d.Tool.Definition.Provider = "other"
		return d
	}}} {
		if _, err := platforms.NewCatalog(factories...); err == nil {
			t.Fatal("missing or duplicate registration accepted")
		}
	}
}

func TestCatalogSnapshotsCannotMutateOtherConsumers(t *testing.T) {
	original := gitlabplatform.Definition()
	catalog, err := platforms.NewCatalog(func() platforms.Definition { return original })
	if err != nil {
		t.Fatal(err)
	}
	original.Type.NotificationChannels[0] = "changed"
	original.Tool.Definition.Environment["GITLAB_TOKEN"] = "changed"
	original.Notifications.Input.EncodeStoredInput = nil
	defs := catalog.Definitions()
	defs[0].Sources[0].ID.Channel = "changed"
	defs[0].Tool.Definition.Dependencies[0].Name = "changed"
	defs[0].Connection.Normalize = nil
	refs, _ := catalog.ReferenceCapabilities("gitlab")
	for op, a := range refs.Actions {
		a.Spec.InputSchema[0] = '!'
		if len(a.Spec.ObjectTypes) > 0 {
			a.Spec.ObjectTypes[0] = "changed"
		}
		delete(refs.Actions, op)
	}
	fresh, _ := catalog.ReferenceCapabilities("gitlab")
	defs = catalog.Definitions()
	if defs[0].Type.NotificationChannels[0] != "events" || defs[0].Tool.Definition.Environment["GITLAB_TOKEN"] != "token" || defs[0].Notifications.Input.EncodeStoredInput == nil || defs[0].Sources[0].ID != gitlab.EventsSourceID || defs[0].Tool.Definition.Dependencies[0].Name != "glab" || defs[0].Connection.Normalize == nil || len(fresh.Actions) == 0 {
		t.Fatal("consumer mutated the catalog")
	}
	for _, a := range fresh.Actions {
		if a.Spec.InputSchema[0] != '{' {
			t.Fatal("reference schema was shared")
		}
	}
}

func TestBuiltinToolRuntimeIsSelectedAndStatic(t *testing.T) {
	called := false
	factory := func() platforms.Definition {
		d := larkplatform.Definition()
		original := d.Tool.Bind
		d.Tool.Bind = func(host platformtools.Host) platformtools.Runtime { called = true; return original(host) }
		return d
	}
	c, err := platforms.NewCatalog(factory)
	if err != nil || called {
		t.Fatal("static declaration bound a runtime", err)
	}
	bind := c.ToolRuntime("lark")
	if bind == nil || called {
		t.Fatal("lookup acquired a resource")
	}
	definitions := c.Definitions()
	definitions[0].Tool.Bind = nil
	if c.ToolRuntime("lark") == nil {
		t.Fatal("copy mutated selected binding")
	}
	if !bind(nil).Help([]string{"--help"}) || !called {
		t.Fatal("selected binding not used")
	}
	empty, err := platforms.NewCatalog()
	if err != nil || empty.ToolRuntime("lark") != nil {
		t.Fatal("unselected implementation available", err)
	}
	_, err = platforms.NewCatalog(func() platforms.Definition { d := larkplatform.Definition(); d.Tool.Bind = nil; return d })
	if err == nil {
		t.Fatal("internal tool without runtime accepted")
	}
	_, err = platforms.NewCatalog(func() platforms.Definition {
		d := scionplatform.Definition()
		d.Tool.Bind = larkplatform.BindTool
		return d
	})
	if err == nil {
		t.Fatal("executable with built-in runtime accepted")
	}
}
