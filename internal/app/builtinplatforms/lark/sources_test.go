package lark_test

import (
	"errors"
	"testing"

	larkplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/lark"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestSourceBindingDefersAccountReaderAndKeepsSearchCodec(t *testing.T) {
	calls := 0
	want := errors.New("reader unavailable")
	scope := lark.QueryScope{Profile: platform.CLIProfile{Brand: "feishu"}, PlatformVersion: 7}
	bindings := larkplatform.SourceBindings{Reader: func(got lark.QueryScope) (lark.MessageReader, error) {
		calls++
		if got != scope {
			t.Fatal("reader scope changed", got)
		}
		return nil, want
	}}
	declaration := larkplatform.Definition().Sources[0]
	unbound := declaration.New()
	bound, err := larkplatform.BindSource(unbound, bindings)
	if err != nil || calls != 0 || bound.Schema() != lark.SearchSchema {
		t.Fatal("binding queried accounts or changed search schema", err, calls)
	}
	if unbound.(*lark.SearchSource).Reader != nil {
		t.Fatal("binding mutated static source")
	}
	if _, err := bound.(*lark.SearchSource).Reader(scope); calls != 1 || !errors.Is(err, want) {
		t.Fatal("bound reader lost error or was not called exactly once", calls, err)
	}
	// Other provider sources must not acquire the live search reader during
	// composition.
	for _, source := range []notification.Source{nil, (*lark.SearchSource)(nil), &gitlab.EventsSource{}} {
		if _, err := larkplatform.BindSource(source, bindings); err == nil {
			t.Fatal("unsupported source accepted", source)
		}
	}
}
