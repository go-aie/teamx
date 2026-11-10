//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestLarkSearchSettingsBrowser(t *testing.T) {
	if os.Getenv("TEAMX_LARK_SCOPE_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	h := newLarkSearchHarness(t)
	cfg := h.m.defaultLarkConfig(h.c.ID)
	cfg.Enabled = false
	cfg.Target = notification.Target{Mode: "inbox", AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	h.configure(cfg)
	chats := []lark.Chat{}
	for _, id := range []string{"oc_group", "oc_2", "oc_3", "oc_4", "oc_5"} {
		chats = append(chats, lark.Chat{ID: id, Mode: "group", Name: "测试群 " + id})
	}
	raw, _ := json.Marshal(lark.ChatPage{Chats: chats})
	if err := os.WriteFile(filepath.Join(h.dir, "directory.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", "testdata/lark_search_settings_browser.cjs", server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	s := h.state()
	if s.Search == nil || s.Config.Groups.Scope != "specified" || len(s.Config.Groups.ChatIDs) != 5 || s.Config.Groups.MessageMode != "all" {
		t.Fatal("browser did not persist selected five groups + all messages")
	}
	t.Log(string(out))
}
