package room

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/lark"
)

func larkImageHarness(t *testing.T, legacy bool) (*larkHarness, MemberIdentity, url.Values, []byte) {
	t.Helper()
	h := newLarkHarness(t)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "image.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(l2Fixture, "path=a[2]", `if a[:2]==['im','+messages-resources-download']:
 assert a[2:8]==['--message-id','om_image','--file-key','img_fixture','--type','image'] and a[8:]==['--output','image.bin','--as','user','--format','json']
 with open(base+'/downloads','a') as f:f.write(os.getcwd()+'\n')
 open(base+'/download-started','w').write('yes')
 if os.path.exists(base+'/block-download'):
  import time;time.sleep(60)
 data=open(base+'/image.png','rb').read();open('image.bin','wb').write(data)
 print(json.dumps({'ok':True,'identity':'user','data':{'saved_path':os.path.abspath('image.bin'),'size_bytes':len(data)}}));sys.exit()
path=a[2]`, 1)
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	m := lark.Message{ID: "om_image", ChatID: "oc_chat", Type: "post", Created: "100000", Updated: "100000", Sender: lark.Sender{ID: "ou_sender", IDType: "open_id", Type: "user"}}
	m.Body.Content = `{"zh_cn":{"content":[[{"tag":"img","image_key":"img_fixture"},{"tag":"text","text":"请分析图中的错误"}]]}}`
	raw, _ := json.Marshal([]lark.Message{m})
	if err := os.WriteFile(filepath.Join(h.dir, "messages.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	if legacy {
		s := h.state()
		for key, entry := range s.Inbox {
			entry.Item.Text = "飞书 · 脱敏会话\nimage_key: img_fixture\n请分析图中的错误\n[附件未读取]"
			s.Inbox[key] = entry
		}
		h.m.mu.Lock()
		err := h.m.notifications.SaveLocked(s)
		h.m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	query := url.Values{"room_id": {id.RoomID}, "room_instance": {id.RoomInstance}, "service_epoch": {h.svc.info.ServiceEpoch}, "caller_id": {"operator"}, "agent_id": {id.AgentID}, "membership_id": {id.MembershipID}, "seq": {"1"}, "path": {"lark-image:img_fixture"}}
	return h, id, query, data.Bytes()
}
