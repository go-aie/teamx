//go:build integration || e2e

package room

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Real Chromium and production HTTP/UI with a synthetic, isolated official CLI.
// No OAuth, native messages, runtime configuration or model calls are involved.
func TestLarkAuthorizationAndMemberRepairBrowser(t *testing.T) {
	if os.Getenv("TEAMX_CONNECT_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	for _, viewport := range []string{"desktop", "mobile"} {
		t.Run(viewport, func(t *testing.T) {
			t.Setenv("USER", humanCaller.ID)
			m, req, c, dir := larkAuthorizationWithFixture(t, assistantConnectFixture)
			// Discovery omits --profile; extend the account fixture with the
			// official CLI's separate read-only profile-list contract.
			raw, err := os.ReadFile(c.CLIProfile.Executable)
			if err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(string(raw), "mode=open(base+'/mode').read()", `
if sys.argv[1:]==['--version']:print('lark-cli version 1.0.99');sys.exit()
if sys.argv[1:]==['profile','list']:
 print(json.dumps([{'name':'fixture','appId':'cli_fixture','brand':'feishu','user':'中文用户','tokenStatus':'valid'}]));sys.exit()
mode=open(base+'/mode').read()`, 1)
			if err := os.WriteFile(filepath.Join(dir, "lark-cli"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			member := MemberState{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
			before := bindCLITool(t, m, req.RoomIdentity, member, c, "lark", 0)
			shared := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("e", 32), Name: "共享飞书", Provider: "lark", URL: c.URL, CLIProfile: c.CLIProfile})
			if _, err := m.verifyPlatform(t.Context(), managedPlatformRequest{ID: shared.ID, Version: shared.Version}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(m)
			defer server.Close()
			m.config.endpoint = server.URL
			connectHTTP(t, m, "authorize", req)
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			browser := exec.CommandContext(ctx, "node", "testdata/lark_authorization_browser.cjs", server.URL, dir, req.OperationID, req.RoomInstance, c.ID, viewport, runtime.GOOS)
			out, err := browser.CombinedOutput()
			if err != nil {
				t.Fatalf("browser: %v\n%s", err, out)
			}
			t.Log(strings.TrimSpace(string(out)))
			done := waitConnectionResult(t, m, req)
			if done.Status != "completed" || done.Account != c.Validation.Account {
				t.Fatal("original authorization did not finish", done)
			}
			m.mu.Lock()
			after, err := m.readToolBindingsLocked(req.RoomIdentity)
			m.mu.Unlock()
			if err != nil || len(after.Bindings) != 1 || after.Revision != before.Revision+1 || after.Bindings[0].ActionAuthorization == nil || len(after.Bindings[0].ActionAuthorization.Grants) != 3 {
				t.Fatal("repair did not upgrade the exact read-only member", err, after)
			}
			replay := connectHTTP(t, m, "authorize", req)
			if !reflect.DeepEqual(done, replay) {
				t.Fatal("browser authorization retry changed its historical receipt")
			}
		})
	}
}
