package room

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserFixtureSpaceRouting(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint("managed=", managed), func(t *testing.T) {
			var handler http.Handler
			var epoch string
			if managed {
				manager := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
				handler, epoch = manager, manager.epoch
			} else {
				svc, err := newService(t.TempDir(), "browser-routing", serviceConfig{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { closeService(t, svc) })
				handler, epoch = svc, svc.info.ServiceEpoch
			}
			server := browserTestServer(t, handler)
			read := func(path string, out any) {
				t.Helper()
				response, err := server.Client().Get(server.URL + path)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatal(path, response.Status)
				}
				if err := json.NewDecoder(response.Body).Decode(out); err != nil {
					t.Fatal(path, err)
				}
			}
			var directory spaceDirectory
			read("/v1/spaces", &directory)
			prefix := spaceRoute(directory.DefaultSpaceID)
			var ui struct {
				SpaceID string `json:"space_id"`
				Managed bool   `json:"managed"`
			}
			read(prefix+"/v1/ui", &ui)
			if ui.SpaceID != directory.DefaultSpaceID || ui.Managed != managed {
				t.Fatal("browser bootstrap used a different Space", ui)
			}
			response, err := server.Client().Get(server.URL + prefix + updatesURL(epoch, ServiceInfo{}))
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				response.Body.Close()
				t.Fatal(response.Status)
			}
			hello := nextUpdateFrame(t, updateFrames(response.Body))
			response.Body.Close()
			if hello.event != "hello" || hello.data["space_id"] != directory.DefaultSpaceID {
				t.Fatal("SSE lost the authorized Space context", hello)
			}
			foreignID, err := newOpaqueID()
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := server.Client().Get(server.URL + spaceRoute(foreignID) + "/v1/ui")
			if err != nil {
				t.Fatal(err)
			}
			foreign.Body.Close()
			if foreign.StatusCode != http.StatusForbidden {
				t.Fatal("fixture admitted an unregistered Space", foreign.Status)
			}
		})
	}
}

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
	authorized := access.wrap(handler)
	// The production page discovers its Space through /v1/spaces and prefixes
	// subsequent requests with /s/<id>/. These service/manager fixtures do not
	// run a spaceServer, so route their single registered Space at this boundary.
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/s/") {
			id, suffix, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/s/"), "/")
			if !ok || id != access.space.SpaceID {
				accessError(w, http.StatusForbidden, "identity_mismatch")
				return
			}
			copyURL := *r.URL
			copyURL.Path, copyURL.RawPath = "/"+suffix, ""
			r = r.Clone(r.Context())
			r.URL = &copyURL
			w = &spaceResponseWriter{ResponseWriter: w, prefix: spaceRoute(id)}
		}
		authorized.ServeHTTP(w, r)
	})
	server := httptest.NewUnstartedServer(router)
	endpoint := "http://" + server.Listener.Addr().String()
	access.internal, access.public, access.activeListen = endpoint, endpoint, server.Listener.Addr().String()
	server.Start()
	t.Cleanup(server.Close)
	return server
}
