package extension_test

import (
	"bytes"
	"testing"

	"github.com/go-aie/teamx/boot"
	"github.com/go-aie/teamx/builtin/platforms/gitlab"
	"github.com/go-aie/teamx/extension"
	"github.com/go-aie/teamx/platform"
	"github.com/go-aie/teamx/platform/notification"
)

func TestCompositionChecksOneToolNamespaceWithoutResolvingPrograms(t *testing.T) {
	local := extension.Extension{Name: "local", Tools: []platform.Tool{{Name: "local", Executable: "/nonexistent/tool"}}}
	if _, err := extension.Compose(gitlab.Extension(), local); err != nil {
		t.Fatal("static composition attempted to resolve a program", err)
	}
	for _, selected := range [][]extension.Extension{
		{{}},
		{local, local},
		{{Name: "local", Tools: []platform.Tool{{Name: "bad name", Executable: "program"}}}},
		{{Name: "local", Tools: []platform.Tool{{Name: "missing-program"}}}},
		{{Name: "local", Tools: []platform.Tool{{Name: "tool", Executable: "a"}, {Name: "tool", Executable: "b"}}}},
		{gitlab.Extension(), {Name: "local", Tools: []platform.Tool{{Name: "gitlab", Executable: "fake"}}}},
	} {
		if _, err := extension.Compose(selected...); err == nil {
			t.Fatal("invalid or conflicting composition accepted")
		}
	}
}

type staticSource struct{}

func (staticSource) ID() notification.SourceID {
	return notification.SourceID{Provider: "feed", Channel: "updates"}
}
func (staticSource) Schema() int { return 1 }
func (staticSource) Decode([]byte, platform.RoomIdentity) (notification.State, error) {
	panic("static check decoded live state")
}
func (staticSource) NewRunner(notification.StepHost) notification.Runner {
	panic("static check started runner")
}

func TestNotificationCompositionIsStaticAndRejectsConflictingSources(t *testing.T) {
	declaration := notification.Declaration{ID: staticSource{}.ID(), New: func() notification.Source { return staticSource{} },
		Bind: func(notification.Source, notification.RuntimeHost) (notification.Runtime, error) {
			panic("static check bound runtime")
		}}
	contribution := func(sources ...notification.Declaration) extension.Extension {
		return extension.Extension{Name: "feed", Notifications: []notification.Provider{{Provider: "feed", Sources: sources}}}
	}
	selected, err := extension.Compose(contribution(declaration))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := boot.Run(selected, []string{"extensions", "check"}, &output, &output); err != nil || !bytes.Contains(output.Bytes(), []byte(`"provider":"feed"`)) {
		t.Fatal(output.String(), err)
	}
	missingBind := declaration
	missingBind.Bind = nil
	wrongID := declaration
	wrongID.ID.Provider = "other"
	for _, invalid := range []extension.Extension{contribution(), contribution(missingBind), contribution(wrongID), contribution(declaration, declaration)} {
		if _, err := extension.Compose(invalid); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	// Sources cannot implicitly replace the built-in selection or register the
	// same provider under a second extension name.
	duplicate := contribution(declaration)
	duplicate.Name = "another"
	if _, err := extension.Compose(contribution(declaration), duplicate); err == nil {
		t.Fatal("duplicate provider accepted")
	}
}
