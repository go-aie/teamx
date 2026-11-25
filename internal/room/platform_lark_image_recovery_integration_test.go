//go:build integration || e2e

package room

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Only the native platform is synthetic. Each restart owns a fresh manager,
// service, Store and HTTP listener in a separate OS process.
func TestLarkImagesRecoveryProcess(t *testing.T) {
	root := os.Getenv("TEAMX_SELF_RECOVERY_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	var room roomSummary
	if err := json.Unmarshal([]byte(os.Getenv("TEAMX_SELF_RECOVERY_ROOM")), &room); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, root, managerTestProfiles(t))
	server := httptest.NewServer(fixtureHTTPHandler(m))
	defer server.Close()
	var svc *service
	if os.Getenv("TEAMX_SELF_RECOVERY_MEMBERS") == "1" {
		var err error
		svc, err = newService(m.roomDir(room.RoomID), room.RoomID, serviceConfig{endpoint: server.URL, cliPath: os.Args[0], persistentScheduling: true})
		if err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		m.runtimes[room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
		m.mu.Unlock()
	} else {
		svc = eventLiveService(t, &eventHarness{m: m, room: room})
	}
	m.bindPlatformReferences(svc)
	ready, _ := json.Marshal(struct {
		Endpoint string
		Info     ServiceInfo
	}{server.URL, svc.info})
	if err := os.WriteFile(os.Getenv("TEAMX_SELF_RECOVERY_READY"), ready, 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestLarkImagesIndependentProcessRecovery(t *testing.T) {
	for _, log := range []bool{false, true} {
		storage := "embedded"
		if log {
			storage = "input-log"
		}
		for _, kind := range []string{"self", "group"} {
			for _, mode := range []string{"settled", "during_upload", "after_dispatch", "before_verification"} {
				// Both formats retain successful recovery for both target kinds.
				// Crash publication uses the same ledger: cover every interruption
				// per format and alternate the target so each target also sees each
				// interruption across the two formats.
				if mode != "settled" {
					group := mode == "during_upload"
					if log {
						group = !group
					}
					if (kind == "group") != group {
						continue
					}
				}
				t.Run(storage+"/"+kind+"/"+mode, func(t *testing.T) {
					parallelFixtureTest(t)
					h, id, server, cli, refs := imageSendHarness(t, log)
					t.Cleanup(func() { _ = os.WriteFile(filepath.Join(h.dir, "release"), nil, 0600) })
					q := imageActionRequest(t, h, id, cli, kind, refs[:2], "完整说明\n\n第二段", "durable-image-intent")
					server.Close()
					if mode != "settled" {
						path := filepath.Join(h.dir, "lark-cli")
						b, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						gate := `import time
open(base+'/blocked','w').write('ready')
while not os.path.exists(base+'/release'):time.sleep(.01)
sys.exit(1)
`
						needle := " print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}));sys.exit()"
						switch mode {
						case "during_upload":
							needle = " index=int(name.split('-')[1].split('.')[0])"
							gate = " if name.startswith('image-2.'):\n" + indentImageGate(gate, "  ")
						case "before_verification":
							needle = "if path=='/open-apis/im/v1/messages/search':"
							gate = "if path=='/open-apis/im/v1/messages/om_receipt':\n" + indentImageGate(gate, " ")
						default:
							gate = indentImageGate(gate, " ")
						}
						if !strings.Contains(string(b), needle) {
							t.Fatal("fixture gate missing")
						}
						if err = os.WriteFile(path, []byte(strings.Replace(string(b), needle, gate+needle, 1)), 0700); err != nil {
							t.Fatal(err)
						}
					}
					root := h.m.store.dir
					if err := h.m.close(); err != nil {
						t.Fatal(err)
					}
					p := startActionRecoveryProcess(t, root, h.room, "TestLarkImagesRecoveryProcess", true)
					cli = contractCLI(t, p.endpoint, &service{info: p.info}, id)
					wantStatus, wantPosts := "succeeded", 1
					if mode == "settled" {
						r, err := executeImageActionAtUpload(t, h, id, cli, q)
						if err != nil || r.Status != wantStatus || r.Result == nil {
							t.Fatal(r, err)
						}
						p.stop(t, false)
					} else {
						done := make(chan error, 1)
						go func() { _, err := executeSelfSend(cli, q); done <- err }()
						waitSelfSendFile(t, filepath.Join(h.dir, "blocked"))
						assertImageActionAttempt(t, h.svc.store, id, q.ID, q.Input, imageRequestDigest(t, h, q.Input))
						// Same-ID concurrency while a native child is paused cannot upload/send again.
						r, err := executeSelfSend(cli, q)
						if err != nil || r.Status != "processing" {
							t.Fatal(r, err)
						}
						p.stop(t, true)
						if err = os.WriteFile(filepath.Join(h.dir, "release"), nil, 0600); err != nil {
							t.Fatal(err)
						}
						select {
						case <-done:
						case <-time.After(5 * time.Second):
							t.Fatal("crashed request retained")
						}
						wantStatus = "unknown"
						if mode == "during_upload" {
							wantPosts = 0
						}
					}
					beforeUploads, _ := os.ReadFile(filepath.Join(h.dir, "uploads"))
					for range 2 {
						p2 := startActionRecoveryProcess(t, root, h.room, "TestLarkImagesRecoveryProcess")
						if p.info.ServiceEpoch == p2.info.ServiceEpoch {
							t.Fatal("process epoch reused")
						}
						cli = contractCLI(t, p2.endpoint, &service{info: p2.info}, id)
						q.TurnNumber = 99
						r, err := executeSelfSend(cli, q)
						if err != nil || r.Status != wantStatus || r.Attempts != 1 {
							t.Fatal(r, err)
						}
						var changed ActionInput
						raw := strings.Replace(string(actionInputJSON(q.Input)), "完整说明", "变更说明", 1)
						if err = json.Unmarshal([]byte(raw), &changed); err != nil {
							t.Fatal(err)
						}
						conflict := q
						conflict.Input = changed
						if _, err = executeSelfSend(cli, conflict); err == nil {
							t.Fatal("changed frozen content accepted")
						}
						p2.stop(t, false)
					}
					afterUploads, _ := os.ReadFile(filepath.Join(h.dir, "uploads"))
					if string(beforeUploads) != string(afterUploads) || len(selfSendPosts(t, h.dir)) != wantPosts {
						t.Fatal("recovery repeated uploads or message POST")
					}
					assertImageUploadedOriginals(t, h, q.Input)
					// Crashed processes lose their in-memory leases; opening the source Store
					// in a new owner must not retain a native or object pin.
					store, err := OpenStore(h.svc.store.dir, h.room.RoomID)
					if err != nil {
						t.Fatal(err)
					}
					defer store.Close()
					assertActionImagePinsReleased(t, store)
					records, err := store.queryActions(id, q.ID, "", "")
					if err != nil || len(records) != 1 || string(actionInputJSON(records[0].Request.Input)) != string(actionInputJSON(q.Input)) || len(records[0].Inspection.ContentDigest) != 64 {
						t.Fatal("complete image intent lost on recovery", err)
					}
				})
			}
		}
	}
}

func indentImageGate(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n"+prefix) + "\n"
}
