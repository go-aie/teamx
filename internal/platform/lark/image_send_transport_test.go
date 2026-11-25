//go:build integration || e2e

package lark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// A protocol peer knows only argv, uploaded bytes, order and native responses.
// It never opens Room state, authorization evidence or an action ledger.
const imageTransportFixture = `
if a[:3]==['im','images','create']:
 import hashlib,stat,time
 assert a[3:5]==['--data','{"image_type":"message"}'] and a[7:]==['--as','user','--format','json']
 assert a[5]=='--file' and a[6].startswith('image=./image-') and a[6].endswith('.png')
 name=a[6][len('image=./'):];assert '/' not in name
 index=int(name.split('-')[1].split('.')[0]);data=open(name,'rb').read()
 assert data==open(base+'/'+name,'rb').read()
 assert stat.S_IMODE(os.stat(name).st_mode)==0o600 and stat.S_IMODE(os.stat(os.getcwd()).st_mode)==0o700
 with open(base+'/uploads','a') as f:f.write(json.dumps({'cwd':os.getcwd(),'index':index,'hash':hashlib.sha256(data).hexdigest()})+'\n')
 if mode=='cancel_upload':
  open(base+'/blocked','w').write('yes');time.sleep(60)
 if mode=='fail-'+str(index):sys.exit(1)
 print(json.dumps({'ok':True,'identity':'user','data':{'image_key':'img_'+hashlib.sha256(data).hexdigest()}}));sys.exit()
if a[:3]==['api','POST','/open-apis/im/v1/messages']:
 import time
 assert a[3]=='--params' and a[5]=='--data' and a[7:]==['--as','user','--format','json']
 expected=json.load(open(base+'/expected'))
 payload=json.loads(a[6]);assert set(payload)=={'receive_id','msg_type','content','uuid'}
 assert json.loads(a[4])=={'receive_id_type':expected['receive_type']}
 assert payload['receive_id']==expected['receive_id'] and payload['uuid']=='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
 assert payload['msg_type']==expected['kind'] and json.loads(payload['content'])==json.loads(expected['content'])
 uploads=[json.loads(line) for line in open(base+'/uploads')]
 assert [v['index'] for v in uploads]==list(range(1,expected['count']+1))
 with open(base+'/posts','a') as f:f.write(json.dumps(payload)+'\n')
 if mode=='cancel_post':
  open(base+'/blocked','w').write('yes');time.sleep(60)
 if mode=='lost_response':sys.exit(1)
 if mode=='damaged':print('broken');sys.exit()
 print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}));sys.exit()
if a[:3]==['api','POST','/open-apis/im/v1/chat_p2p/batch_query']:
 assert json.loads(a[4])=={'chatter_id_type':'open_id'} and json.loads(a[6])=={'chatter_ids':['ou_reader']}
 print(json.dumps({'ok':True,'identity':'user','data':{'p2p_chats':[{'chat_id':'oc_self'}]}}));sys.exit()
if a[:2]==['api','GET'] and a[2].startswith('/open-apis/im/v1/chats/'):
 data={'items':[{'member_id':user,'member_id_type':'open_id','tenant_key':'tenant'}],'has_more':False} if a[2].endswith('/members') else {'chat_mode':'p2p' if 'oc_self' in a[2] else 'group','tenant_key':'tenant','name':'fixture','chat_status':'normal','external':False,'group_message_type':'chat','moderation_permission':'all_members'}
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
raise Exception('unexpected operation')
`

func TestPinnedImageSendProtocol(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS pinned backend")
	}
	for _, tc := range []struct {
		mode, target, body string
		count              int
	}{
		{"single", "user", "", 1}, {"multiple", "user", "", 4}, {"caption", "chat", "\n中文 **literal** @人 ./file\n\n", 4},
		{"fail-1", "user", "", 4}, {"fail-2", "user", "", 4}, {"fail-3", "user", "", 4}, {"fail-4", "user", "", 4},
		{"guard", "user", "", 2}, {"dispatch", "chat", "", 2}, {"cancel_upload", "user", "", 2},
		{"lost_response", "chat", "", 2}, {"damaged", "chat", "", 2}, {"cancel_post", "chat", "", 2},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			parallelClientTest(t)
			p, dir := authTestProfile(t)
			script := strings.Replace(pinnedFixture, "'contact:user:search']", "'contact:user:search','im:resource']", 1)
			script = strings.Replace(script, "raise Exception('unexpected operation')", imageTransportFixture, 1)
			if err := os.WriteFile(p.Executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			setMode(t, dir, tc.mode)
			account := permissionAccount()
			pin, err := NewClientFactory(0, 0, 0).Pin(t.Context(), p, account)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			message := PreparedImageMessage{Body: tc.body}
			keys := []string{}
			for i := range tc.count {
				var b bytes.Buffer
				if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, i+2, i+3))); err != nil {
					t.Fatal(err)
				}
				data := b.Bytes()
				sum := sha256.Sum256(data)
				digest := hex.EncodeToString(sum[:])
				message.Images = append(message.Images, SendImage{Data: data, MIME: "image/png", SHA256: digest})
				keys = append(keys, "img_"+digest)
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("image-%d.png", i+1)), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			target := platform.TargetIdentity{IdentityScope: account.IdentityScope, Object: platform.NativeIdentity{Type: tc.target, ID: platform.ExternalID{Namespace: nativeNamespace(account, "chat_id"), Value: "oc_group"}}}
			receiveType := "chat_id"
			if tc.target == "user" {
				target.Object.ID = account.ID
				target.Containers = []platform.NativeIdentity{{Type: "chat", ID: platform.ExternalID{Namespace: nativeNamespace(account, "chat_id"), Value: "oc_self"}}}
				receiveType = "open_id"
			}
			// Independent expected payload: no production imagePayload helper here.
			kind := "post"
			var content any
			if tc.count == 1 && tc.body == "" {
				kind = "image"
				content = map[string]string{"image_key": keys[0]}
			} else {
				rows := [][]map[string]string{}
				if tc.body != "" {
					for _, line := range strings.Split(tc.body, "\n") {
						rows = append(rows, []map[string]string{{"tag": "text", "text": line}})
					}
				}
				for _, key := range keys {
					rows = append(rows, []map[string]string{{"tag": "img", "image_key": key}})
				}
				content = map[string]any{"zh_cn": map[string]any{"title": "", "content": rows}}
			}
			raw, _ := json.Marshal(content)
			expected, _ := json.Marshal(map[string]any{"receive_type": receiveType, "receive_id": target.Object.ID.Value, "kind": kind, "content": string(raw), "count": tc.count})
			if err := os.WriteFile(filepath.Join(dir, "expected"), expected, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cancelDone := make(chan struct{})
			if strings.HasPrefix(tc.mode, "cancel_") {
				go func() {
					defer close(cancelDone)
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							if _, err := os.Stat(filepath.Join(dir, "blocked")); err == nil {
								cancel()
								return
							}
						}
					}
				}()
			} else {
				close(cancelDone)
			}
			dispatched := 0
			id, gotKeys, err := pin.SendImages(ctx, target, message, strings.Repeat("a", 32), func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if tc.mode == "guard" {
					if _, err := os.Stat(filepath.Join(dir, "uploads")); err == nil {
						return errors.New("revoked after upload")
					}
				}
				return nil
			}, func(ctx context.Context, digest string) error {
				if digest != message.ContentDigest() {
					t.Error("dispatch changed prepared digest")
				}
				dispatched++
				if tc.mode == "dispatch" {
					return errors.New("frozen intent changed")
				}
				return ctx.Err()
			})
			cancel()
			<-cancelDone
			uploads, _ := os.ReadFile(filepath.Join(dir, "uploads"))
			lines := bytes.Split(bytes.TrimSpace(uploads), []byte("\n"))
			wantUploads := tc.count
			if strings.HasPrefix(tc.mode, "fail-") {
				_, _ = fmt.Sscanf(tc.mode, "fail-%d", &wantUploads)
			}
			if tc.mode == "guard" || tc.mode == "cancel_upload" {
				wantUploads = 1
			}
			if len(lines) != wantUploads {
				t.Fatalf("uploads=%d want=%d: %s", len(lines), wantUploads, uploads)
			}
			for i, line := range lines {
				var item struct {
					Cwd   string
					Index int
					Hash  string
				}
				if json.Unmarshal(line, &item) != nil || item.Index != i+1 || item.Hash != message.Images[i].SHA256 {
					t.Fatal("upload bytes/order", string(line))
				}
				if _, err := os.Stat(item.Cwd); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("temporary files retained", item.Cwd, err)
				}
			}
			posts, _ := os.ReadFile(filepath.Join(dir, "posts"))
			uncertain := tc.mode == "lost_response" || tc.mode == "damaged" || tc.mode == "cancel_post"
			success := tc.mode == "single" || tc.mode == "multiple" || tc.mode == "caption"
			wantPosts := 0
			if success || uncertain {
				wantPosts = 1
			}
			if bytes.Count(posts, []byte("\n")) != wantPosts {
				t.Fatal("partial/duplicate POST", string(posts))
			}
			wantDispatch := wantPosts
			if tc.mode == "dispatch" {
				wantDispatch = 1
			}
			if dispatched != wantDispatch {
				t.Fatal("dispatch boundary", dispatched, wantDispatch)
			}
			if success {
				if err != nil || id != "om_receipt" || strings.Join(gotKeys, ",") != strings.Join(keys, ",") {
					t.Fatal(id, gotKeys, err)
				}
			} else if uncertain {
				if err == nil || errors.Is(err, ErrSendNotSent) || id != "" || len(gotKeys) != tc.count {
					t.Fatal("POST outcome uncertainty lost", id, gotKeys, err)
				}
			} else {
				if !errors.Is(err, ErrSendNotSent) || id != "" || len(gotKeys) != 0 {
					t.Fatal("pre-POST failure misclassified", id, gotKeys, err)
				}
			}
			if strings.HasPrefix(tc.mode, "fail-") && ErrorCode(err) != fmt.Sprintf("image_upload_%d_failed", wantUploads) {
				t.Fatal("upload error classification", err)
			}
		})
	}
}
