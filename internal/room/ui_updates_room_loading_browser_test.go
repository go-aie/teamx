//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestWebRoomLoadingBrowser(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_BROWSER") != "1" {
		t.Skip("optional Chromium Room loading acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	ensureAssistant(t, m)
	m.factory = controlledActivationFactory(nil, nil)
	a := assistantSummary(t, m)
	selection := directSelection("loading-target")
	selection.Name = "Loading target"
	b := createDirectConversation(t, m, selection)
	for _, target := range []roomSummary{a, b} {
		svc := openRoomOK(t, m, target, "loading-open-"+target.RoomID)
		req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "loading-message", Text: "History " + target.RoomID}
		if w := managerHTTP(m, "/v1/send", req); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	server := browserTestServer(t, m)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	identities, _ := json.Marshal([]RoomIdentity{a.RoomIdentity, b.RoomIdentity})
	cmd := exec.CommandContext(ctx, "node", "testdata/web_room_loading.cjs", server.URL, string(identities))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Room loading: %v\n%s", err, output)
	}
	t.Log(string(output))
}
