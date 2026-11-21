package platforms

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestBuiltinCatalogAndDeferredRuntimeBinding(t *testing.T) {
	catalog, err := Builtins()
	if err != nil {
		t.Fatal(err)
	}
	want := []platform.TypeDefinition{
		{Provider: "gitlab", ToolType: "gitlab", NotificationChannels: []string{"events", "todos"}},
		{Provider: "lark", ToolType: "lark", NotificationChannels: []string{"messages"}},
		{Provider: "scion", ToolType: "scion"},
	}
	if !reflect.DeepEqual(catalog.Types().Types(), want) {
		t.Fatal(catalog.Types().Types())
	}
	resolved := []string{}
	tools := catalog.Tools(func(name, env string) string { resolved = append(resolved, name+":"+env); return "/selected/" + name })
	if len(tools) != 3 || !tools[1].InternalOnly || tools[1].Executable != "" || tools[1].Provider != "lark" || !reflect.DeepEqual(tools[0].Environment, map[string]string{"GITLAB_HOST": "instance", "GITLAB_TOKEN": "token"}) ||
		!reflect.DeepEqual(resolved, []string{"teamx-gitlab:TEAMX_GITLAB_BIN", "teamx-scion:TEAMX_SCION_BIN"}) || tools[0].Executable != "/selected/teamx-gitlab" {
		t.Fatal(tools, resolved)
	}
	calls := 0
	for _, d := range catalog.Definitions() {
		for _, source := range d.Sources {
			bound := source.Bind(SourceBindings{GitLabEvents: func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
				calls++
				return nil, 0, nil
			}})
			if bound.ID() != source.ID {
				t.Fatal(bound.ID(), source.ID)
			}
		}
	}
	if calls != 0 {
		t.Fatal("static source binding started a query")
	}
	for _, provider := range []string{"gitlab", "lark"} {
		n := catalog.NotificationCapabilities(provider)
		if n.Input == nil || n.Input.Codec.Descriptor().Provider != provider {
			t.Fatal("codec missing", provider)
		}
		refs, ok := catalog.ReferenceCapabilities(provider)
		if !ok || len(refs.Actions) == 0 {
			t.Fatal("references missing", provider)
		}
	}
	refs, _ := catalog.ReferenceCapabilities("lark")
	for _, op := range []platform.Operation{platform.OperationMessageSendSelf, platform.OperationMessageSend, platform.OperationMessageReply} {
		cap := refs.Actions[op]
		if cap.Spec.DisabledReason != "" || strings.Contains(string(cap.Spec.InputSchema), `"images"`) != (op != platform.OperationMessageReply) {
			t.Fatal("formal Lark content schema does not match accepted target modes", op, cap.Spec)
		}
	}
	gitlabRefs, _ := catalog.ReferenceCapabilities("gitlab")
	for _, op := range []platform.Operation{platform.OperationObjectComment, platform.OperationDiscussionReply} {
		cap := gitlabRefs.Actions[op]
		if cap.Spec.DisabledReason != "" || !strings.Contains(string(cap.Spec.InputSchema), `"images"`) || cap.Spec.DirectRoomInstructionOnly {
			t.Fatal("formal GitLab discovery must advertise images without restricting plain-text authority", op, cap.Spec)
		}
	}
	if n := catalog.NotificationCapabilities("scion"); n.Input != nil {
		t.Fatal("tool-only provider acquired an input codec")
	}
	if _, ok := catalog.ReferenceCapabilities("scion"); ok {
		t.Fatal("tool-only provider acquired references")
	}
}

func notificationOnly() Definition {
	return Definition{Type: platform.TypeDefinition{Provider: "gitlab", NotificationChannels: []string{"events"}}, Sources: []Source{{ID: gitlab.EventsSourceID, Bind: func(SourceBindings) notification.Source { return &gitlab.EventsSource{} }}}}
}
func toolOnly() Definition { return scionDefinition() }

func TestOptionalCapabilitiesRemainIndependent(t *testing.T) {
	catalog, err := NewCatalog(notificationOnly, toolOnly)
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
		change func(*Definition)
	}{
		{"missing tool", func(d *Definition) { d.Tool = nil }},
		{"undeclared tool", func(d *Definition) { d.Type.ToolType = "" }},
		{"wrong tool provider", func(d *Definition) { d.Tool.Definition.Provider = "other" }},
		{"wrong tool type", func(d *Definition) { d.Tool.Definition.Name = "other" }},
		{"missing executable", func(d *Definition) { d.Tool.Executable = "" }},
		{"missing source", func(d *Definition) { d.Sources = d.Sources[:1] }},
		{"undeclared source", func(d *Definition) { d.Type.NotificationChannels = []string{"events"} }},
		{"duplicate channel", func(d *Definition) { d.Type.NotificationChannels = append(d.Type.NotificationChannels, "events") }},
		{"duplicate source", func(d *Definition) { d.Sources = append(d.Sources, d.Sources[0]) }},
		{"missing source factory", func(d *Definition) { d.Sources[0].Bind = nil }},
		{"nil source", func(d *Definition) { d.Sources[0].Bind = func(SourceBindings) notification.Source { return nil } }},
		{"typed nil source", func(d *Definition) {
			d.Sources[0].Bind = func(SourceBindings) notification.Source { return (*gitlab.EventsSource)(nil) }
		}},
		{"wrong source implementation", func(d *Definition) {
			d.Sources[0].Bind = func(SourceBindings) notification.Source { return &gitlab.TodosSource{} }
		}},
		{"incomplete codec", func(d *Definition) { d.Notifications.Input.EncodeStoredInput = nil }},
		{"wrong codec provider", func(d *Definition) { d.Notifications.Input = larkDefinition().Notifications.Input }},
		{"incomplete admission", func(d *Definition) { d.Notifications.Admission = &notification.AdmissionCapability{} }},
		{"empty references", func(d *Definition) { d.References = &platform.ReferenceCapabilities{} }},
		{"missing input codec", func(d *Definition) { d.Notifications.Input.Codec = nil }},
		{"missing input encoder", func(d *Definition) { d.Notifications.Input.EncodeStoredInput = nil }},
		{"missing input decoder", func(d *Definition) { d.Notifications.Input.DecodeStoredInput = nil }},
		{"wrong source implementation", func(d *Definition) {
			d.Sources[0].Bind = func(SourceBindings) notification.Source { return &gitlab.TodosSource{} }
		}},
		{"missing normalizer", func(d *Definition) { d.Connection.Normalize = nil }},
		{"inconsistent action", func(d *Definition) {
			a := d.References.Actions[platform.OperationObjectComment]
			a.Spec.Operation = platform.OperationMessageSend
			d.References.Actions[platform.OperationObjectComment] = a
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCatalog(func() Definition { d := gitlabDefinition(); tc.change(&d); return d })
			if err == nil {
				t.Fatal("inconsistent registration accepted")
			}
		})
	}
	for _, factories := range [][]Factory{{nil}, {gitlabDefinition, gitlabDefinition}, {func() Definition { return Definition{} }}, {toolOnly, func() Definition {
		d := toolOnly()
		d.Type.Provider = "other"
		d.Tool.Definition.Provider = "other"
		return d
	}}} {
		if _, err := NewCatalog(factories...); err == nil {
			t.Fatal("missing or duplicate registration accepted")
		}
	}
}

func TestCatalogSnapshotsCannotMutateOtherConsumers(t *testing.T) {
	original := gitlabDefinition()
	catalog, err := NewCatalog(func() Definition { return original })
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
