//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

// Real management HTTP and Store, synthetic notifications, no model/platform.
func TestInboxPageBrowser(t *testing.T) {
	if os.Getenv("TEAMX_INBOX_BROWSER") != "1" {
		t.Skip("optional local browser acceptance")
	}
	output := os.Getenv("TEAMX_INBOX_BROWSER_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("fresh absolute evidence directory required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, filepath.Join(output, "rooms"), managerTestProfiles(t))
	type target struct {
		Service ServiceInfo      `json:"service"`
		Members []MemberIdentity `json:"members"`
	}
	targets := make([]target, 0, 2)
	services := map[string]*service{}
	for _, name := range []string{"inbox-browser-A", "inbox-browser-B"} {
		response := managerHTTP(m, "/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: roomSelection{CreateID: name, TeamID: "takeout-dinner", Name: name}})
		var room roomSummary
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &room) != nil {
			t.Fatal(response.Code, response.Body.String())
		}
		svc := eventLiveService(t, &eventHarness{m: m, room: room})
		state, err := svc.store.LoadRoom()
		if err != nil {
			t.Fatal(err)
		}
		group := target{Service: svc.info}
		for _, member := range state.Members {
			group.Members = append(group.Members, MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID})
		}
		if len(group.Members) < 2 {
			t.Fatal("two fixture members required")
		}
		targets = append(targets, group)
		services[room.RoomID] = svc
	}
	appendItems := func(ctx context.Context, svc *service, id MemberIdentity, count int) error {
		raw, err := svc.inspectInbox(ctx, apiRequest{Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID})
		if err != nil {
			return err
		}
		latest := raw.(InboxStatus).LatestSeq
		for i := 1; i <= count; i++ {
			seq := latest + uint64(i)
			post := integrationPost(id.RoomIdentity, int64(seq))
			item := notification.Item{Key: post.Source.ItemKey, DeliveryID: post.DeliveryID, Text: fmt.Sprintf("浏览器验收通知 #%d\n\n这是隔离 Store 的合成通知，用于检查浏览位置和来源展开状态。", seq), Source: post.Source, CreatedAt: time.Now().UTC(), Route: notification.Route{Target: inboxTarget(id), Version: 1}}
			if err := svc.receiveInbox(ctx, inboxTarget(id), item); err != nil {
				return err
			}
		}
		return nil
	}
	for i, group := range targets {
		for j, id := range group.Members {
			count := 2
			if i == 0 && j == 0 {
				count = 60
			}
			if err := appendItems(context.Background(), services[id.RoomID], id, count); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Test-only controls drive durable reception and reclamation through real actors.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__inbox_fixture" {
			m.ServeHTTP(w, r)
			return
		}
		var input struct {
			Room    int    `json:"room"`
			Member  int    `json:"member"`
			Count   int    `json:"count"`
			Through uint64 `json:"through"`
		}
		if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&input) != nil || input.Room < 0 || input.Room >= len(targets) || input.Member < 0 || input.Member >= len(targets[input.Room].Members) || input.Count < 0 || input.Count > 5 {
			http.Error(w, "invalid fixture command", 400)
			return
		}
		id := targets[input.Room].Members[input.Member]
		svc := services[id.RoomID]
		err := appendItems(r.Context(), svc, id, input.Count)
		if err == nil && input.Through > 0 {
			svc.inboxMu.Lock()
			addr, e := svc.inboxLocked(r.Context(), id)
			if e == nil {
				_, e = addr.Call[InboxStatus](r.Context(), ReclaimInbox{id, input.Through})
			}
			svc.inboxMu.Unlock()
			err = e
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	ready := struct {
		Endpoint string   `json:"endpoint"`
		Targets  []target `json:"targets"`
	}{server.URL, targets}
	readyFile := filepath.Join(output, "ready.json")
	if err := os.WriteFile(readyFile, mustContractJSON(ready), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/inbox_page_browser.cjs", readyFile, output)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(string(out))
	for _, group := range targets {
		for _, id := range group.Members {
			raw, err := services[id.RoomID].inspectInbox(context.Background(), apiRequest{Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID})
			if err != nil {
				t.Fatal(err)
			}
			status := raw.(InboxStatus)
			if status.ConsumedThrough != 0 || status.Batch != nil {
				t.Fatal("browser changed Agent input progress", status)
			}
		}
	}
}
