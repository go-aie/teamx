package platforms_test

import (
	"reflect"
	"strings"
	"testing"

	builtinplatforms "github.com/go-aie/teamx/builtin/platforms"
	"github.com/go-aie/teamx/extension"
	gitlabplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/gitlab"
	larkplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/lark"
	"github.com/go-aie/teamx/internal/app/extensions"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestBuiltinCatalogAndDeferredRuntimeBinding(t *testing.T) {
	selected, err := extension.Compose(builtinplatforms.Extensions()...)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := extensions.Catalog(selected)
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
	for _, d := range catalog.Definitions() {
		for _, source := range d.Sources {
			unbound := source.New()
			var bound notification.Source
			switch source.ID.Provider {
			case "gitlab":
				bound, err = gitlabplatform.BindSource(unbound, gitlabplatform.SourceBindings{})
			case "lark":
				bound, err = larkplatform.BindSource(unbound, larkplatform.SourceBindings{})
			}
			if err != nil || bound == nil || bound.ID() != source.ID || bound.Schema() != unbound.Schema() {
				t.Fatal("runtime binding differs from declaration", source.ID, err)
			}
		}
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
