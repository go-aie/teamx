//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Fresh manager, Store locks, service and HTTP listener in each OS process.
// Recovery deliberately has no functioning platform preparer/transport.
func TestGitlabImagesRecoveryProcess(t *testing.T) {
	root := os.Getenv("TEAMX_SELF_RECOVERY_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	var room roomSummary
	if err := json.Unmarshal([]byte(os.Getenv("TEAMX_SELF_RECOVERY_ROOM")), &room); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, root, managerTestProfiles(t))
	server := httptest.NewServer(m)
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
		m.referenceExecutors["gitlab"] = func(context.Context, MemberIdentity, platform.AccessContext) (referenceActionSession, error) {
			_ = os.WriteFile(filepath.Join(root, "unexpected-prepare"), []byte("called"), 0600)
			return referenceActionSession{}, ErrIdentity
		}
	} else {
		svc = eventLiveService(t, &eventHarness{m: m, room: room})
		destination, err := url.Parse(os.Getenv("TEAMX_GITLAB_RECOVERY_SINK"))
		if err != nil || destination.Scheme != "http" || !strings.HasPrefix(destination.Host, "127.0.0.1:") {
			t.Fatal("loopback fixture required")
		}
		m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 30*time.Second {
				t.Error("missing child request deadline")
			}
			copy := r.Clone(r.Context())
			u := *r.URL
			u.Scheme, u.Host = destination.Scheme, destination.Host
			copy.URL = &u
			return http.DefaultTransport.RoundTrip(copy)
		})
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

func TestGitlabImagesIndependentProcessRecovery(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		for _, mode := range []string{"settled", "during_upload", "after_dispatch", "before_verification"} {
			t.Run(strings.Join([]string{map[bool]string{false: "comment", true: "thread"}[discussion], mode}, "/"), func(t *testing.T) {
				f := gitlabImageActionFixture(t, discussion)
				f.setInput(t, "完整说明\n第二段", f.refs[:2])
				q := gitlabAgentWire(f.action)
				blocked, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				if mode != "settled" {
					f.hook = func(r *http.Request) {
						hit := mode == "during_upload" && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/uploads") && f.uploads.Load() == 1 || mode == "before_verification" && strings.HasSuffix(r.URL.Path, "/notes/99")
						if hit {
							close(blocked)
							<-release
						}
					}
				}
				transport := f.m.platformHTTP
				var pending sync.WaitGroup
				bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					pending.Add(1)
					defer pending.Done()
					ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
					defer cancel()
					copy := r.Clone(ctx)
					u := *r.URL
					u.Scheme, u.Host = "https", "gitlab.invalid"
					copy.URL = &u
					copy.RequestURI = ""
					response, err := transport.RoundTrip(copy)
					if err != nil {
						http.Error(w, "fixture transport", 502)
						return
					}
					defer response.Body.Close()
					if mode == "after_dispatch" && r.Method == "POST" && !strings.HasSuffix(r.URL.Path, "/uploads") {
						close(blocked)
						<-release
					}
					for key, values := range response.Header {
						w.Header()[key] = values
					}
					w.WriteHeader(response.StatusCode)
					_, _ = io.Copy(w, response.Body)
				}))
				defer bridge.Close()
				t.Setenv("TEAMX_GITLAB_RECOVERY_SINK", bridge.URL)
				root := f.m.store.dir
				f.server.Close()
				if err := f.m.close(); err != nil {
					t.Fatal(err)
				}
				p := startActionRecoveryProcess(t, root, f.room, "TestGitlabImagesRecoveryProcess", true)
				cli := contractCLI(t, p.endpoint, &service{info: p.info}, f.id)
				execute := func(cli func([]string, any) error, request any) (agentGitlabReceipt, error) {
					raw, _ := json.Marshal(request)
					var receipt agentGitlabReceipt
					err := cli([]string{"actions", "execute", "--request", string(raw)}, &receipt)
					return receipt, err
				}
				want, posts := "succeeded", int32(1)
				if mode == "settled" {
					receipt, err := execute(cli, q)
					if err != nil || receipt.Status != want || !receipt.Result.Verified {
						t.Fatal(receipt, err)
					}
					p.stop(t, false)
				} else {
					done := make(chan error, 1)
					go func() { _, err := execute(cli, q); done <- err }()
					select {
					case <-blocked:
					case <-time.After(15 * time.Second):
						t.Fatal("dispatch gate not reached")
					}
					// Same-ID concurrency cannot start another upload. Different frozen input
					// is rejected even while the first call has not returned.
					receipt, err := execute(cli, q)
					if err != nil || receipt.Status != "processing" {
						t.Fatal(receipt, err)
					}
					duplicate := gitlabAgentWire(f.action)
					duplicate["id"] = "parallel-same-content"
					if _, err = execute(cli, duplicate); err == nil {
						t.Fatal("parallel new ID bypassed unknown protection")
					}
					var inflight actionQueryRecord
					if err = cli([]string{"actions", "read", "--id", f.action.ID}, &inflight); err != nil || inflight.Detail == nil || inflight.Detail.Attempts != 1 {
						t.Fatal("ledger read blocked behind network", err)
					}
					changed := gitlabAgentWire(f.action)
					changed["input"] = ActionInput{Body: "changed"}
					if _, err = execute(cli, changed); err == nil {
						t.Fatal("inflight input mutation accepted")
					}
					p.stop(t, true)
					unblock()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("crashed request retained")
					}
					want = "unknown"
					if mode == "during_upload" {
						posts = 0
					}
				}
				pending.Wait()
				f.requests.Wait()
				uploads := f.uploads.Load()
				for range 2 {
					p2 := startActionRecoveryProcess(t, root, f.room, "TestGitlabImagesRecoveryProcess")
					if p.info.ServiceEpoch == p2.info.ServiceEpoch {
						t.Fatal("epoch reused")
					}
					cli = contractCLI(t, p2.endpoint, &service{info: p2.info}, f.id)
					q["turn_number"] = 99
					receipt, err := execute(cli, q)
					if err != nil || receipt.Status != want || receipt.Attempts != 1 {
						t.Fatal(receipt, err)
					}
					var detail actionQueryRecord
					if err = cli([]string{"actions", "read", "--id", f.action.ID}, &detail); err != nil || detail.Status != want || detail.Detail == nil || len(detail.Detail.Inspection.ContentDigest) != 64 || string(actionInputJSON(detail.Detail.Request.Input)) != string(actionInputJSON(f.action.Input)) {
						t.Fatal("complete original intent unavailable", err)
					}
					changed := gitlabAgentWire(f.action)
					changed["input"] = ActionInput{Body: "changed"}
					if _, err = execute(cli, changed); err == nil {
						t.Fatal("changed frozen content accepted")
					}
					p2.stop(t, false)
				}
				if f.uploads.Load() != uploads || f.posts.Load() != posts {
					t.Fatal("restart continued upload or POST", f.uploads.Load(), uploads, f.posts.Load(), posts)
				}
				if _, err := os.Stat(filepath.Join(root, "unexpected-prepare")); !os.IsNotExist(err) {
					t.Fatal("receipt recovery entered preparer")
				}
				store, err := OpenStore(f.svc.store.dir, f.room.RoomID)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				assertActionImagePinsReleased(t, store)
			})
		}
	}
}
