package lark

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestPinnedSendTransportSinglePOSTAndCancellation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS pinned backend")
	}
	for _, kind := range []string{"user", "chat"} {
		t.Run(kind, func(t *testing.T) {
			for _, mode := range []string{"ok", "missing", "guard_revoked", "damaged", "cancel_after_dispatch"} {
				t.Run(mode, func(t *testing.T) {
					p, dir := authTestProfile(t)
					script := strings.Replace(pinnedFixture, "raise Exception('unexpected operation')", `if a[:3]==['api','POST','/open-apis/im/v1/messages']:
 assert a[-4:]==['--as','user','--format','json']
 kind=open(base+'/kind').read()
 assert json.loads(a[4])=={'receive_id_type':'open_id' if kind=='user' else 'chat_id'}
 payload=json.loads(a[6]);assert payload['msg_type']=='text' and payload['receive_id']==('ou_peer' if kind=='user' else 'oc_group')
 assert 'reply_in_thread' not in payload
 assert payload['uuid']=='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
 assert json.loads(payload['content'])=={'text':'@./literal\n"精确"'}
 with open(base+'/posts','a') as f:f.write(json.dumps(a)+'\n')
 if mode=='cancel_after_dispatch':
  import time
  time.sleep(60)
 if mode=='damaged':print('broken');sys.exit()
 print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}));sys.exit()
raise Exception('unexpected operation')`, 1)
					if err := os.WriteFile(p.Executable, []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(dir+"/kind", []byte(kind), 0600); err != nil {
						t.Fatal(err)
					}
					setMode(t, dir, mode)
					account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
					pin, err := Pin(context.Background(), p, account)
					if err != nil {
						t.Fatal(err)
					}
					defer pin.Close()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					done := make(chan struct{})
					if mode == "cancel_after_dispatch" {
						go func() {
							defer close(done)
							ticker := time.NewTicker(5 * time.Millisecond)
							defer ticker.Stop()
							for {
								select {
								case <-ctx.Done():
									return
								case <-ticker.C:
									if info, err := os.Stat(filepath.Join(dir, "posts")); err == nil && info.Size() > 0 {
										cancel()
										return
									}
								}
							}
						}()
					} else {
						close(done)
					}
					target := platform.TargetIdentity{IdentityScope: account.IdentityScope, Containers: []platform.NativeIdentity{{Type: "chat", ID: platform.ExternalID{Namespace: nativeNamespace(account, "chat_id"), Value: "oc_private"}}}, Object: platform.NativeIdentity{Type: "user", ID: platform.ExternalID{Namespace: account.ID.Namespace, Value: "ou_peer"}}}
					if kind == "chat" {
						target.Containers = nil
						target.Object = platform.NativeIdentity{Type: "chat", ID: platform.ExternalID{Namespace: nativeNamespace(account, "chat_id"), Value: "oc_group"}}
					}
					id, err := pin.SendText(ctx, target, "@./literal\n\"精确\"", strings.Repeat("a", 32), func(context.Context) error {
						if mode == "guard_revoked" {
							return errors.New("revoked")
						}
						return nil
					})
					cancel()
					<-done
					data, _ := os.ReadFile(filepath.Join(dir, "posts"))
					lines := strings.Split(strings.TrimSpace(string(data)), "\n")
					count := len(lines)
					if len(data) == 0 {
						count = 0
					}
					want := 1
					if mode == "missing" || mode == "guard_revoked" {
						want = 0
					}
					if count != want {
						t.Fatal("write count", count, want, err)
					}
					switch mode {
					case "ok":
						if err != nil || id != "om_receipt" {
							t.Fatal(id, err)
						}
					case "missing", "guard_revoked":
						if !errors.Is(err, ErrSendNotSent) {
							t.Fatal(err)
						}
					default:
						if err == nil || errors.Is(err, ErrSendNotSent) {
							t.Fatal("dispatched request claimed not sent", err)
						}
					}
					if count > 0 {
						var argv []string
						if json.Unmarshal([]byte(lines[0]), &argv) != nil {
							t.Fatal("bad fixture evidence")
						}
					}
				})
			}
		})
	}
}
