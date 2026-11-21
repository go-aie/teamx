//go:build e2e && darwin

package room

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLarkImagePermissionSettingsBrowser(t *testing.T) {
	if os.Getenv("TEAMX_LARK_IMAGE_SETTINGS_BROWSER") != "1" {
		t.Skip("opt-in Chromium with local CLI fixture")
	}
	for _, mode := range []string{"ready", "application-missing"} {
		t.Run(mode, func(t *testing.T) {
			h, _, fixtureServer, _, _ := imageSendHarness(t)
			fixtureServer.Close()
			path := filepath.Join(h.dir, "lark-cli")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(string(b), "import sys,json,os\n", `import sys,json,os
if sys.argv[1:]==['--version']:print('lark-cli version 1.0.99');sys.exit()
if sys.argv[1:]==['profile','list']:
 print(json.dumps([{'name':'fixture','brand':'feishu','appId':'cli_fixture','user':'测试用户','tokenStatus':'valid'}]));sys.exit()
`, 1)
			if mode != "ready" {
				script = strings.Replace(script, ",'im:resource']", "]", 1)
			}
			if err = os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			server := browserTestServer(t, h.m)
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			out := t.TempDir()
			if root := os.Getenv("TEAMX_LARK_IMAGE_BROWSER_OUTPUT"); root != "" {
				if !filepath.IsAbs(root) {
					t.Fatal("absolute browser evidence directory required")
				}
				out = filepath.Join(root, mode)
				if err := os.MkdirAll(out, 0700); err != nil {
					t.Fatal(err)
				}
			}
			output, err := exec.CommandContext(ctx, "node", "testdata/lark_image_settings_browser.cjs", server.URL, h.c.ID, mode, out, h.svc.info.RoomID, h.svc.info.RoomInstance).CombinedOutput()
			if err != nil {
				t.Fatal(err, string(output))
			}
			t.Log(string(output))
			if len(selfSendPosts(t, h.dir)) != 0 {
				t.Fatal("settings inspection sent a message")
			}
			if b, _ := os.ReadFile(filepath.Join(h.dir, "uploads")); len(b) != 0 {
				t.Fatal("settings inspection uploaded an image")
			}
		})
	}
}
