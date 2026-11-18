//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"sync"
	"testing"
	"time"
)

func TestNotificationOwnersBrowserRefreshAndSwitch(t *testing.T) {
	if os.Getenv("TEAMX_NOTIFICATION_BROWSER") != "1" {
		t.Skip("optional source owner browser acceptance")
	}
	runRoomsCatalogBrowser(t, true)
}
func TestRoomsCatalogBrowserRefreshAndSwitch(t *testing.T) {
	if os.Getenv("TEAMX_ROOMS_CATALOG_BROWSER") != "1" {
		t.Skip("optional Chromium catalog acceptance")
	}
	runRoomsCatalogBrowser(t, false)
}

func runRoomsCatalogBrowser(t *testing.T, sourceOwner bool) {
	t.Setenv("USER", humanCaller.ID)
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	ensureAssistant(t, m)
	m.factory = controlledActivationFactory(nil, nil)
	a := assistantSummary(t, m)
	selection := directSelection("catalog-browser-other")
	selection.Name = "Catalog other"
	b := createDirectConversation(t, m, selection)
	for _, target := range []roomSummary{a, b} {
		svc := openRoomOK(t, m, target, "catalog-open-"+target.RoomID)
		if w := managerHTTP(m, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "history", Text: "History " + target.RoomID}); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	release, held, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var start sync.Once
	hold := func() { m.mu.Lock(); close(held); <-release; m.mu.Unlock(); close(done) }
	if sourceOwner {
		m.mu.Lock()
		svc, _ := m.runtimeServiceLocked(a.RoomIdentity)
		m.mu.Unlock()
		roster, err := svc.room.Call[MemberList](t.Context(), ListMembers{a.RoomIdentity})
		if err != nil || len(roster.Members) == 0 {
			t.Fatal("browser member unavailable", err)
		}
		member := roster.Members[0]
		binding := *testGitLabWorkspace().Binding
		binding.PlatformID, binding.PlatformGeneration = strings.Repeat("a", 32), strings.Repeat("b", 32)
		cfg := gitlab.EventsConfig{Enabled: true, PlatformID: binding.PlatformID, Project: binding.Project, Target: notification.Target{Mode: "inbox", AgentID: member.AgentID, MembershipID: member.MembershipID}}
		state, err := gitlab.ConfigureEvents(a.RoomIdentity, nil, &binding, cfg, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		state.UserID, state.Username = 7, "fixture"
		if err := m.notifications.SaveLocked(state); err != nil {
			t.Fatal(err)
		}
		save := m.store.ops.syncDir
		var blocked sync.Once
		m.store.ops.syncDir = func(dir string) error {
			if dir == filepath.Join(m.roomDir(a.RoomID), "notifications") {
				blocked.Do(func() { close(held); <-release })
			}
			return save(dir)
		}
		hold = func() {
			next := state.Clone()
			next.LastCheck = time.Now()
			err := m.notifications.SaveFrom(next, state)
			if err != nil {
				t.Errorf("source save: %v", err)
			}
			close(done)
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__catalog/hold":
			start.Do(func() { go hold() })
			<-held
			w.WriteHeader(http.StatusNoContent)
		case "/__catalog/release":
			unblock()
			<-done
			w.WriteHeader(http.StatusNoContent)
		default:
			m.ServeHTTP(w, r)
		}
	})
	server := browserTestServer(t, m, handler)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	identities, _ := json.Marshal([]RoomIdentity{a.RoomIdentity, b.RoomIdentity})
	cmd := exec.CommandContext(ctx, "node", "testdata/rooms_catalog_browser.cjs", server.URL, string(identities), fmt.Sprint(sourceOwner))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("catalog browser: %v\n%s", err, output)
	}
	t.Log(string(output))
}
