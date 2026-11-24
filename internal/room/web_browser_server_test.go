package room

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Browser fixtures use the same local, login-disabled access middleware as a
// production service. SSE deliberately rejects handlers without that context.
func browserTestServer(t *testing.T, owner http.Handler, handlers ...http.Handler) *httptest.Server {
	t.Helper()
	access, _, _ := accessFixture(t)
	access.disabled = true
	bindFixtureAccessSubject(access, humanOperator())
	switch host := owner.(type) {
	case *service:
		host.access = access
	case *roomManager:
		host.config.access = access
	default:
		t.Fatal("browser access requires a service or manager")
	}
	handler := owner
	if len(handlers) > 0 {
		handler = handlers[0]
	}
	server := httptest.NewUnstartedServer(access.wrap(handler))
	endpoint := "http://" + server.Listener.Addr().String()
	access.internal, access.public, access.activeListen = endpoint, endpoint, server.Listener.Addr().String()
	server.Start()
	t.Cleanup(server.Close)
	return server
}
