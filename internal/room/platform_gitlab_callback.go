package room

import (
	"context"
	"sync"
)

// Callback routes are transient transport bindings for both private and shared
// applications. They locate an original runtime; that runtime still verifies
// the browser, owner, application and authorization before exchanging a code.
type gitlabCallbackRoutes struct {
	mu     sync.Mutex
	routes map[string]gitlabCallbackRoute
}

type gitlabCallbackRoute struct {
	space SpaceContext
	ctx   context.Context
}

func (routes *gitlabCallbackRoutes) register(state string, space SpaceContext, ctx context.Context) error {
	routes.mu.Lock()
	defer routes.mu.Unlock()
	if state == "" || ctx.Err() != nil {
		return ErrIdentity
	}
	if _, exists := routes.routes[state]; exists {
		return ErrConflict
	}
	if routes.routes == nil {
		routes.routes = map[string]gitlabCallbackRoute{}
	}
	routes.routes[state] = gitlabCallbackRoute{space: space, ctx: ctx}
	return nil
}

func (routes *gitlabCallbackRoutes) lookup(state string) (SpaceContext, bool) {
	routes.mu.Lock()
	defer routes.mu.Unlock()
	route, ok := routes.routes[state]
	if !ok || route.ctx.Err() != nil {
		delete(routes.routes, state)
		return SpaceContext{}, false
	}
	return route.space, true
}

func (routes *gitlabCallbackRoutes) remove(state string) {
	routes.mu.Lock()
	defer routes.mu.Unlock()
	delete(routes.routes, state)
}

func (m *roomManager) gitlabCallbackRoutes() *gitlabCallbackRoutes {
	if a := m.config.access; a != nil && a.spaces != nil {
		return &a.spaces.gitlabCallbacks
	}
	return nil
}

func (m *roomManager) removeGitLabCallbackLocked(s *gitlabAuthSession) {
	if routes := m.gitlabCallbackRoutes(); routes != nil {
		routes.remove(s.state)
	}
}
