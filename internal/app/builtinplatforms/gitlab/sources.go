package gitlab

import (
	"fmt"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// SourceBindings contains only the existing bounded query operations. Binding
// never invokes them or changes the catalog's unbound source.
type SourceBindings struct {
	Events gitlab.EventsPrepare
	Todos  gitlab.TodosPrepare
}

func BindSource(source notification.Source, bindings SourceBindings) (notification.Source, error) {
	switch source := source.(type) {
	case *gitlab.EventsSource:
		if source != nil {
			bound := *source
			bound.Prepare = bindings.Events
			return &bound, nil
		}
	case *gitlab.TodosSource:
		if source != nil {
			bound := *source
			bound.Prepare = bindings.Todos
			return &bound, nil
		}
	}
	return nil, fmt.Errorf("gitlab: unsupported notification source %T", source)
}
