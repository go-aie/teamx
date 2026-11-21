package gitlab_test

import (
	"context"
	"errors"
	"testing"

	gitlabplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/gitlab"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestSourceBindingDefersQueriesAndPreservesStaticSources(t *testing.T) {
	calls := 0
	want := errors.New("query unavailable")
	ctx := context.Background()
	bindings := gitlabplatform.SourceBindings{
		Events: func(got context.Context, dir string, _ gitlab.Binding) (gitlab.EventFetcher, int64, error) {
			if got != ctx || dir != "events" {
				t.Fatal("event query scope changed")
			}
			calls++
			return nil, 7, want
		},
		Todos: func(got context.Context, dir string, _ platform.RoomIdentity, _ gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
			if got != ctx || dir != "todos" {
				t.Fatal("todo query scope changed")
			}
			calls++
			return nil, want
		},
	}
	for _, declaration := range gitlabplatform.Definition().Sources {
		unbound := declaration.New()
		bound, err := gitlabplatform.BindSource(unbound, bindings)
		if err != nil || calls != 0 {
			t.Fatal("binding executed a query", err, calls)
		}
		switch source := bound.(type) {
		case *gitlab.EventsSource:
			if unbound.(*gitlab.EventsSource).Prepare != nil {
				t.Fatal("binding mutated static source")
			}
			_, revision, err := source.Prepare(ctx, "events", gitlab.Binding{})
			if revision != 7 || !errors.Is(err, want) {
				t.Fatal(revision, err)
			}
		case *gitlab.TodosSource:
			if unbound.(*gitlab.TodosSource).Prepare != nil {
				t.Fatal("binding mutated static source")
			}
			if _, err := source.Prepare(ctx, "todos", platform.RoomIdentity{}, gitlab.TodosQueryScope{}); !errors.Is(err, want) {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unexpected bound source %T", bound)
		}
		if calls != 1 {
			t.Fatal("bound query was not called exactly once", calls)
		}
		calls = 0
	}
	for _, source := range []notification.Source{nil, (*gitlab.EventsSource)(nil), (*gitlab.TodosSource)(nil), &lark.SearchSource{}} {
		if _, err := gitlabplatform.BindSource(source, bindings); err == nil {
			t.Fatal("nil source accepted")
		}
	}
}
