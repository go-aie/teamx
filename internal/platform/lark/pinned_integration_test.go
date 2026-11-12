//go:build integration || e2e

package lark

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestPinnedAccountIsolatesExternalProfileAndFailsOnRevocation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS pinning contract")
	}
	p, dir := authTestProfile(t)
	if err := os.WriteFile(p.Executable, []byte(pinnedFixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", dir)
	t.Setenv("LARKSUITE_CLI_PROFILE", "other-user")
	t.Setenv("LARKSUITE_CLI_WORKSPACE", "other-workspace")
	t.Setenv("LARKSUITE_CLI_USER_ACCESS_TOKEN", "must-not-inherit")
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
	pin, err := NewClientFactory(0, 0, 0).Pin(context.Background(), p, account)
	if err != nil {
		t.Fatal(err)
	}
	root := pin.client.configDir
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("private directory", info, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "token") || strings.Contains(string(content), "must-not-inherit") {
		t.Fatal("credential escaped")
	}
	// Changing ordinary profile metadata does not affect the admitted config.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"apps":[{"appId":"cli_other","users":[{"userOpenId":"ou_other"}]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ok", "refresh", "missing", "target-missing", "search-missing"} {
		setMode(t, dir, mode)
		v, err := pin.Verify(context.Background())
		if err != nil || v.Account != account {
			t.Fatal(mode, v, err)
		}
		permission, err := pin.SendPermission(context.Background())
		if err != nil || !permission.AccountFixed || !permission.AdapterImplemented || permission.ReplyEnabled != (mode != "missing") || permission.GroupSendEnabled != (permission.ReplyEnabled && mode != "target-missing") || permission.SelfSendEnabled != permission.GroupSendEnabled || permission.PeopleSearchEnabled != (mode != "search-missing") || permission.UserSendEnabled || (mode == "missing" && permission.UserScopes != "missing") {
			t.Fatal(permission, err)
		}
	}
	setMode(t, dir, "revoked")
	if _, err := pin.Verify(context.Background()); err == nil {
		t.Fatal("revocation silently selected another account")
	}
	pin.Close()
	pin.Close()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("pinned state retained", err)
	}
}

func TestSelfSendPinnedQueueCancellationAndFinalGuard(t *testing.T) {
	parallelClientTest(t)
	if runtime.GOOS != "darwin" {
		t.Skip("macOS pinning contract")
	}
	profile, dir := authTestProfile(t)
	script := strings.Replace(pinnedFixture, "raise Exception('unexpected operation')", `if a[:3]==['api','POST','/open-apis/im/v1/messages']:
 with open(base+'/posts','a') as f:f.write('post\n')
 print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}));sys.exit()
raise Exception('unexpected operation')`, 1)
	if err := os.WriteFile(profile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
	clients := NewClientFactory(0, 0, 0)
	pins := make([]*Pinned, 2)
	for i := range pins {
		pin, err := clients.Pin(t.Context(), profile, account)
		if err != nil {
			t.Fatal(err)
		}
		pins[i] = pin
		defer pin.Close()
	}
	key := account.Instance + "|" + account.ID.Namespace + "|" + account.ID.Value
	ctx := context.WithValue(t.Context(), accountContextKey{}, key)
	release, err := clients.selfSend.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := platform.TargetIdentity{IdentityScope: account.IdentityScope, Object: platform.NativeIdentity{Type: "user", ID: account.ID}, Containers: []platform.NativeIdentity{{Type: "chat", ID: platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_self"}}}}
	cancelled, cancel := context.WithCancel(ctx)
	done := make(chan error, 2)
	go func() {
		_, e := pins[0].SendText(cancelled, target, "cancelled", strings.Repeat("a", 32), func(context.Context) error { return nil })
		done <- e
	}()
	awaitQueue(t, clients.selfSend, key, 1)
	var allowed atomic.Bool
	allowed.Store(true)
	go func() {
		_, e := pins[1].SendText(ctx, target, "revoked while waiting", strings.Repeat("b", 32), func(context.Context) error {
			if !allowed.Load() {
				return errors.New("binding revoked")
			}
			return nil
		})
		done <- e
	}()
	awaitQueue(t, clients.selfSend, key, 2)
	cancel()
	allowed.Store(false)
	release()
	for range 2 {
		if e := <-done; !errors.Is(e, ErrSendNotSent) {
			t.Fatal("cancelled/revoked queue entry was dispatched", e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "posts")); !os.IsNotExist(err) {
		t.Fatal("queue revocation reached POST", err)
	}
	// Cancelled waiters release capacity and cannot strand the shared account.
	if id, err := pins[0].SendText(ctx, target, "new authorized task", strings.Repeat("c", 32), func(context.Context) error { return nil }); err != nil || id != "om_receipt" {
		t.Fatal(id, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "posts")); err != nil || strings.TrimSpace(string(b)) != "post" {
		t.Fatal("unexpected native dispatches", string(b), err)
	}
}

func TestLoginIncludesCurrentReaderAndSendDependencies(t *testing.T) {
	p, dir := authTestProfile(t)
	if _, err := Login(context.Background(), p, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	var argv []string
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		_ = json.Unmarshal([]byte(line), &argv)
		for i, a := range argv {
			if a == "--scope" {
				scope := argv[i+1]
				for _, required := range DefaultScopes() {
					if !slices.Contains(strings.Fields(scope), required) {
						t.Fatal(scope)
					}
				}
			}
		}
	}
	if strings.Contains(readerScopes, "send_as_user") {
		t.Fatal("reader dependency includes write scopes")
	}
}

func TestPinnedSendDependencyFailurePreventsDispatchWithoutBlockingReply(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS pinning contract")
	}
	profile, dir := authTestProfile(t)
	script := strings.Replace(pinnedFixture, "raise Exception('unexpected operation')", `if a[:2]==['api','POST']:
 assert a[2] in ['/open-apis/im/v1/messages','/open-apis/im/v1/messages/om_original/reply']
 with open(base+'/posts','a') as f:f.write(a[2]+'\n')
 print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}));sys.exit()
raise Exception('unexpected operation')`, 1)
	if err := os.WriteFile(profile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	pin, err := NewClientFactory(0, 0, 0).Pin(t.Context(), profile, permissionAccount())
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	target := platform.TargetIdentity{IdentityScope: pin.Account.IdentityScope, Object: platform.NativeIdentity{Type: "chat", ID: platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_group"}}}
	guard := func(context.Context) error { return nil }
	setMode(t, dir, "target-missing")
	if _, err := pin.SendText(t.Context(), target, "blocked", strings.Repeat("a", 32), guard); !errors.Is(err, ErrSendNotSent) {
		t.Fatal("target scope failure reached dispatch", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "posts")); !os.IsNotExist(err) {
		t.Fatal("unexpected POST", err)
	}
	if id, err := pin.ReplyText(t.Context(), "om_original", "reply", strings.Repeat("b", 32), guard); err != nil || id != "om_receipt" {
		t.Fatal("target scope failure blocked reply", id, err)
	}
	setMode(t, dir, "search-missing")
	if id, err := pin.SendText(t.Context(), target, "send", strings.Repeat("c", 32), guard); err != nil || id != "om_receipt" {
		t.Fatal("optional search scope failure blocked send", id, err)
	}
	posts, err := os.ReadFile(filepath.Join(dir, "posts"))
	if err != nil || strings.Count(string(posts), "\n") != 2 {
		t.Fatal("unexpected dispatch count", string(posts), err)
	}
}
