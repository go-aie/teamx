//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Block the final output write so ownership can be inspected without relying
// on TCP buffer sizes or a timing window. Nested calls end with an exit frame;
// the root action endpoint returns one JSON response.
type actionOutputGate struct {
	*httptest.ResponseRecorder
	framed  bool
	started chan struct{}
	release chan struct{}
	once    sync.Once
	stop    sync.Once
}

func (w *actionOutputGate) unblock() { w.stop.Do(func() { close(w.release) }) }
func (w *actionOutputGate) Write(data []byte) (int, error) {
	last := !w.framed
	if w.framed {
		var frame toolFrame
		last = json.Unmarshal(data, &frame) == nil && frame.Stream == "exit"
	}
	if last {
		w.once.Do(func() { close(w.started) })
		<-w.release
	}
	return w.ResponseRecorder.Write(data)
}

func TestLarkActionReceiptOutputOwnership(t *testing.T) {
	parallelFixtureTest(t)
	h := selfSendHarnessWithMembers(t, 1)
	id, _ := enableSelfSend(t, h, h.members[0], 0)
	server := httptest.NewServer(h.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, h.svc, id, h.c.ToolEntryID)
	q := selfSendRequest(t, cli, "output-ownership")
	var receipts []json.RawMessage
	for _, nested := range []bool{false, true} {
		name, path := "root", "/v1/actions/execute"
		var request any = apiRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), Tool: h.c.ToolEntryID, ReferenceAction: &q}
		if nested {
			name, path = "nested", "/v1/tools/run"
			request = toolsRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), Tool: h.c.ToolEntryID, Args: []string{"actions", "execute", "--request", string(mustContractJSON(q))}}
		}
		t.Run(name, func(t *testing.T) {
			w := &actionOutputGate{ResponseRecorder: httptest.NewRecorder(), framed: nested, started: make(chan struct{}), release: make(chan struct{})}
			defer w.unblock()
			done := make(chan struct{})
			r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(append(mustContractJSON(request), '\n')))
			go func() { defer close(done); h.m.ServeHTTP(w, r) }()
			select {
			case <-w.started:
			case <-done:
				t.Fatal("request ended before its final output write", w.Body.String())
			case <-time.After(10 * time.Second):
				t.Fatal("final output write was not reached")
			}
			h.m.mu.Lock()
			held := h.m.platformCalls.Len()
			h.m.mu.Unlock()
			t.Logf("final output blocked: platform_calls=%d", held)
			if nested && held != 1 {
				t.Fatal("nested action released ownership before its exit frame", held)
			}
			// Durable success is already recoverable even though delivery of the
			// response has not finished. Reading the ledger must never resend.
			var saved actionQueryRecord
			if err := cli([]string{"actions", "read", "--id", q.ID}, &saved); err != nil || saved.Status != "succeeded" || saved.Attempts != 1 || saved.Detail == nil || saved.Detail.Access.PlatformID != h.c.ID {
				t.Fatal("blocked response lost the original durable receipt", err)
			}
			w.unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("output did not settle")
			}
			if w.Code != http.StatusOK || h.m.platformCalls.Len() != 0 {
				t.Fatal("output failed or retained a Call", w.Code)
			}
			data := w.Body.Bytes()
			if nested {
				var stdout bytes.Buffer
				decoder := json.NewDecoder(bytes.NewReader(data))
				for {
					var frame toolFrame
					if err := decoder.Decode(&frame); err == io.EOF {
						break
					} else if err != nil {
						t.Fatal(err)
					}
					if frame.Stream == "stdout" {
						stdout.Write(frame.Data)
					}
				}
				data = stdout.Bytes()
			}
			var receipt json.RawMessage
			if json.Unmarshal(data, &receipt) != nil {
				t.Fatal("invalid action output")
			}
			receipts = append(receipts, receipt)
		})
	}
	if len(receipts) != 2 {
		t.Fatal("missing transport result")
	}
	var root, nested any
	if json.Unmarshal(receipts[0], &root) != nil || json.Unmarshal(receipts[1], &nested) != nil || !reflect.DeepEqual(root, nested) || len(selfSendPosts(t, h.dir)) != 1 {
		t.Fatal("entry replay changed the receipt or resent the action")
	}
}
