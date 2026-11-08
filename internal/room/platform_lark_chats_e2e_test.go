//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
)

func TestLarkChatScopeBrowser(t *testing.T) {
	if os.Getenv("TEAMX_LARK_SCOPE_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	h := newLarkHarness(t)
	page := lark.ChatPage{Chats: []lark.Chat{{ID: "oc_private", Mode: "p2p", Name: "同事"}, {ID: "oc_group", Mode: "group", Name: "项目群"}}}
	raw, _ := json.Marshal(page)
	if err := os.WriteFile(filepath.Join(h.dir, "directory.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/lark_scope_browser.cjs", server.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestLarkChatSearchBrowser(t *testing.T) {
	if os.Getenv("TEAMX_LARK_SCOPE_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	h := newLarkHarness(t)
	installChatSearchFixture(t, h)
	raw, _ := json.Marshal(lark.ChatPage{Chats: []lark.Chat{{ID: "oc_private", Mode: "p2p", Name: "同事"}, {ID: "oc_group", Mode: "group", Name: "项目群"}}})
	if err := os.WriteFile(filepath.Join(h.dir, "directory.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", "testdata/lark_search_browser.cjs", server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
