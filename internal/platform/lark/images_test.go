package lark

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeAndSavedImageReferences(t *testing.T) {
	m := sampleMessage("om_one", "oc_chat", "image_key: img_fake")
	if len(m.ImageKeys()) != 0 {
		t.Fatal("text became native image evidence")
	}
	m.Type = "post"
	m.Body.Content = `{"zh_cn":{"content":[[{"tag":"img","image_key":"img_one"},{"tag":"text","text":"image_key: img_fake"},{"tag":"img","image_key":"img_two"},{"tag":"img","image_key":"img_one"}]]}}`
	if got := strings.Join(m.ImageKeys(), ","); got != "img_one,img_two,img_one" {
		t.Fatal(got)
	}
	s := sampleState(t)
	src := sourceRef(s, m, "key", Chat{ID: m.ChatID})
	for _, saved := range []string{m.Content(), "image_key: img_one\nimage_key: img_two\nimage_key: img_one"} {
		found := FindNotificationImages(saved, src, 100)
		if found.References != 3 || len(found.Images) != 2 || len(found.Images[0].Positions) != 2 {
			t.Fatal(found)
		}
		found = FindNotificationImages(saved, src, 1)
		if found.References != 3 || found.Omitted != 2 || len(found.Images) != 1 {
			t.Fatal(found)
		}
	}
	bad := "`![x](lark-image:img_code)`\n\n![x](https://evil.invalid/img_one)\n![x](lark-image:img_one?token=x)\n![x](lark-image:../img_one)"
	if found := FindNotificationImages(bad, src, 100); found.References != 0 {
		t.Fatal(found)
	}
	m.Deleted = true
	if len(m.ImageKeys()) != 0 || strings.Contains(m.Content(), "img_one") {
		t.Fatal("deleted image exposed")
	}
}

// Extend the existing executable fixture so all identity/gate/process checks are
// production code; binary output remains separate from the JSON stdout envelope.
func imageTestClient(t *testing.T, mode string) (*Client, string, []byte) {
	t.Helper()
	c, dir, _ := testClient(t)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(fixture, "assert a[:2]==['api','GET']", `if a[:2]==['im','+messages-resources-download']:
 assert a[2:8]==['--message-id','om_one','--file-key','img_one','--type','image'] and a[8:]==['--output','image.bin','--as','user','--format','json']
 open(base+'/download_dir','w').write(os.getcwd())
 if mode=='image_block':time.sleep(60)
 if mode=='image_stream':
  with open('image.bin','wb') as f:
   while True:f.write(b'x'*65536);f.flush();time.sleep(.001)
 data=open(base+'/image.png','rb').read() if mode!='image_format' else b'<html>private error</html>'
 open('image.bin','wb').write(data)
 open(base+'/downloaded','w').write('yes')
 print(json.dumps({'ok':True,'identity':'user','data':{'saved_path':os.path.abspath('image.bin'),'size_bytes':len(data)+(1 if mode=='image_bad_size' else 0)}}));sys.exit()
assert a[:2]==['api','GET']`, 1)
	script = strings.Replace(script, "mode=='switch' and os.path.exists(base+'/read')", "(mode=='switch' and os.path.exists(base+'/read')) or (mode=='image_switch' and os.path.exists(base+'/downloaded'))", 1)
	script = strings.Replace(script, "if mode=='wrong_chat':", `if mode.startswith('image'):
 m['msg_type']='image';m['body']['content']='{"image_key":"img_one"}'
 if mode=='image_wrong_key':m['body']['content']='{"image_key":"img_other"}'
 if mode=='image_deleted':m['deleted']=True
 if mode=='image_wrong_chat':m['chat_id']='oc_other'
if mode=='wrong_chat':`, 1)
	if err := os.WriteFile(c.profile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	setMode(t, dir, mode)
	return c, dir, data.Bytes()
}
