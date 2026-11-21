package room

import (
	gitlabplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/gitlab"
	larkplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/lark"
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Only execution dependencies still owned by Room are supplied here. The
// selected catalog remains the sole source selection; unused bindings do nothing.
// External sources use their declared Bind directly, without editing this map.
// GitLab's application lifecycle remains with its original owner until P2r.
func notificationRuntimeBindings(m *roomManager) map[notification.SourceID]func(platforms.Source) notification.Bind {
	if m == nil {
		return nil
	}
	gitlabBinding := func(selected platforms.Source) notification.Bind {
		return func(source notification.Source, _ notification.RuntimeHost) (notification.Runtime, error) {
			adapter := m.gitlabNotificationLifecycle(selected.ID)
			var err error
			adapter.Source, err = gitlabplatform.BindSource(source, gitlabplatform.SourceBindings{Events: m.preparePlatformEvents, Todos: m.preparePlatformTodos})
			return adapter, err
		}
	}
	return map[notification.SourceID]func(platforms.Source) notification.Bind{
		gitlab.EventsSourceID: gitlabBinding,
		gitlab.TodosSourceID:  gitlabBinding,
		lark.MessagesSourceID: func(selected platforms.Source) notification.Bind {
			return func(source notification.Source, host notification.RuntimeHost) (notification.Runtime, error) {
				bound, err := larkplatform.BindSource(source, larkplatform.SourceBindings{Reader: func(q lark.QueryScope) (lark.MessageReader, error) {
					return m.larkClients.New(q.Profile)
				}})
				if err != nil {
					return notification.Runtime{}, err
				}
				return selected.Bind(bound, host)
			}
		},
	}
}
