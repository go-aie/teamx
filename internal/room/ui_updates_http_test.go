package room

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func updatesURL(epoch string, info ServiceInfo) string {
	q := url.Values{"service_epoch": {epoch}}
	if info != (ServiceInfo{}) {
		q.Set("room_id", info.RoomID)
		q.Set("room_instance", info.RoomInstance)
		q.Set("room_service_epoch", info.ServiceEpoch)
	}
	return "/v1/updates?" + q.Encode()
}

type updateFrame struct {
	event string
	data  map[string]any
	err   error
}

func updateFrames(body io.Reader) <-chan updateFrame {
	frames := make(chan updateFrame, 128)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(body)
		var f updateFrame
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				frames <- f
				f = updateFrame{}
				continue
			}
			if strings.HasPrefix(line, "id:") {
				frames <- updateFrame{err: fmt.Errorf("unexpected SSE replay id")}
				return
			}
			if value, ok := strings.CutPrefix(line, "event: "); ok {
				f.event = value
			}
			if value, ok := strings.CutPrefix(line, "data: "); ok {
				f.err = json.Unmarshal([]byte(value), &f.data)
			}
		}
		if err := scanner.Err(); err != nil {
			frames <- updateFrame{err: err}
		}
	}()
	return frames
}

func nextUpdateFrame(t *testing.T, frames <-chan updateFrame) updateFrame {
	t.Helper()
	select {
	case f, ok := <-frames:
		if !ok || f.err != nil {
			t.Fatalf("stream ended: %v", f.err)
		}
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("update frame timed out")
		return updateFrame{}
	}
}

func awaitUpdateClose(t *testing.T, frames <-chan updateFrame) {
	t.Helper()
	select {
	case f, ok := <-frames:
		if ok && f.err == nil {
			t.Fatal("unexpected frame instead of close", f)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not canceled promptly")
	}
}

func updateRequest(t *testing.T, endpoint, method, path string, cookie *http.Cookie, body any) *http.Response {
	t.Helper()
	var data bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&data).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, endpoint+path, &data)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if method != "GET" {
		r.Header.Set("Origin", endpoint)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	r = r.WithContext(ctx)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func updateHTTPService(t *testing.T, managed bool) (*service, *roomManager, *serverAccess, *httptest.Server, *http.Cookie, string) {
	t.Helper()
	a, token, _ := accessFixture(t)
	cookie := accessLogin(t, a.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), token)
	var manager *roomManager
	config := serviceConfig{access: a}
	if managed {
		manager = testManager(t, t.TempDir(), nil)
		manager.config.access = a
		config.updates = manager.updates
	}
	svc, err := newService(t.TempDir(), "updates", config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeService(t, svc) })
	var handler http.Handler = svc
	if managed {
		manager.setRuntimeServiceLocked(svc.info.RoomIdentity, svc)
		handler = manager
	}
	server := httptest.NewUnstartedServer(a.wrap(handler))
	endpoint := "http://" + server.Listener.Addr().String()
	a.internal, a.public, a.activeListen = endpoint, endpoint, server.Listener.Addr().String()
	// Prove that the SSE adapter overrides only its own ordinary server write timeout.
	server.Config.WriteTimeout = time.Second
	server.Start()
	t.Cleanup(server.Close)
	// Stop streams before httptest.Server waits for open connections.
	t.Cleanup(func() {
		svc.freeze()
		if manager != nil {
			manager.updates.close()
		}
	})
	return svc, manager, a, server, cookie, token
}

func TestUIUpdatesHTTPMessageReadAndReconnect(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint("managed=", managed), func(t *testing.T) {
			svc, m, a, server, cookie, _ := updateHTTPService(t, managed)
			epoch := svc.info.ServiceEpoch
			if m != nil {
				epoch = m.epoch
			}
			// Global-only subscription must not receive Room-private invalidations.
			global := updateRequest(t, server.URL, "GET", updatesURL(epoch, ServiceInfo{}), cookie, nil)
			gframes := updateFrames(global.Body)
			nextUpdateFrame(t, gframes)
			resp := updateRequest(t, server.URL, "GET", updatesURL(epoch, svc.info), cookie, nil)
			if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
				t.Fatal(resp.Status, resp.Header)
			}
			frames := updateFrames(resp.Body)
			hello := nextUpdateFrame(t, frames)
			if hello.event != "hello" || hello.data["protocol"] != float64(1) || hello.data["service_epoch"] != epoch || hello.data["installation_id"] != a.installationID {
				t.Fatal(hello)
			}
			body := apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: a.operator}}, ClientMessageID: "once", Text: "never in the SSE frame"}
			sent := updateRequest(t, server.URL, "POST", "/v1/send", cookie, body)
			if sent.StatusCode != 200 {
				b, _ := io.ReadAll(sent.Body)
				t.Fatal(sent.Status, string(b))
			}
			sent.Body.Close()
			found := false
			for range 4 {
				f := nextUpdateFrame(t, frames)
				if f.event != "invalidate" || f.data["connection_id"] != hello.data["connection_id"] || f.data["text"] != nil {
					t.Fatal(f)
				}
				if f.data["topic"] == "messages" {
					found = true
					if f.data["room_service_epoch"] != svc.info.ServiceEpoch {
						t.Fatal(f)
					}
				}
			}
			if !found {
				t.Fatal("no messages invalidation")
			}
			if f := nextUpdateFrame(t, gframes); f.data["topic"] != "catalog" || f.data["room_id"] != nil {
				t.Fatal(f)
			}
			read := updateRequest(t, server.URL, "POST", "/v1/read", cookie, apiRequest{ServiceInfo: svc.info, Caller: body.Caller, Mode: ReadAll})
			var page ReadPage
			if json.NewDecoder(read.Body).Decode(&page) != nil || len(page.Messages) != 1 || page.Messages[0].Text != body.Text {
				t.Fatal(read.Status, page)
			}
			read.Body.Close()
			resp.Body.Close()
			// A disconnected interval is recovered through authoritative HTTP, not replay.
			body.ClientMessageID = "offline"
			body.Text = "committed while disconnected"
			sent = updateRequest(t, server.URL, "POST", "/v1/send", cookie, body)
			if sent.StatusCode != 200 {
				t.Fatal(sent.Status)
			}
			sent.Body.Close()
			reconnected := updateRequest(t, server.URL, "GET", updatesURL(epoch, svc.info), cookie, nil)
			newHello := nextUpdateFrame(t, updateFrames(reconnected.Body))
			if newHello.data["connection_id"] == hello.data["connection_id"] {
				t.Fatal("connection identity reused")
			}
			read = updateRequest(t, server.URL, "POST", "/v1/read", cookie, apiRequest{ServiceInfo: svc.info, Caller: body.Caller, Mode: ReadAll})
			if json.NewDecoder(read.Body).Decode(&page) != nil || len(page.Messages) != 2 {
				t.Fatal(page)
			}
			read.Body.Close()
		})
	}
}

func TestUIUpdatesHTTPResyncAndAuthentication(t *testing.T) {
	svc, _, a, server, cookie, token := updateHTTPService(t, false)
	path := updatesURL(svc.info.ServiceEpoch, svc.info)
	for _, suffix := range []string{"&operator=other", "&caller_id=other", "&token=secret", "&service_epoch=duplicate", "&malformed=%zz"} {
		r := updateRequest(t, server.URL, "GET", path+suffix, cookie, nil)
		if r.StatusCode == 200 {
			t.Fatal("untrusted subscription accepted", suffix)
		}
		r.Body.Close()
	}
	if r := updateRequest(t, server.URL, "GET", path, nil, nil); r.StatusCode != 401 {
		t.Fatal(r.Status)
	}
	execution, revoke, err := a.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "agent", ID: "a"}, MembershipID: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	for _, bearer := range []string{token, execution} {
		r, _ := http.NewRequest("GET", server.URL+path, nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("bearer gained browser subscription")
		}
	}
	resp := updateRequest(t, server.URL, "GET", path, cookie, nil)
	frames := updateFrames(resp.Body)
	nextUpdateFrame(t, frames)
	for i := 0; i < 65; i++ {
		u := roomUIUpdate("activity", svc.info)
		u.AgentID = "a"
		u.MembershipID = fmt.Sprint(i)
		svc.updates.mark(u)
	}
	if f := nextUpdateFrame(t, frames); f.event != "resync" || len(f.data) != 1 {
		t.Fatal(f)
	}
	// Overflow does not shut down the producer or require replay state.
	svc.updates.mark(roomUIUpdate("messages", svc.info))
	if f := nextUpdateFrame(t, frames); f.event != "invalidate" {
		t.Fatal(f)
	}
	logout := updateRequest(t, server.URL, "POST", "/v1/access/logout", cookie, map[string]any{})
	if logout.StatusCode != 200 {
		t.Fatal(logout.Status)
	}
	logout.Body.Close()
	awaitUpdateClose(t, frames)
}

func TestUIUpdatesHTTPRuntimeAndServiceLifetime(t *testing.T) {
	svc, m, _, server, cookie, _ := updateHTTPService(t, true)
	old := svc.info
	old.ServiceEpoch = "obsolete"
	for _, info := range []ServiceInfo{old, {RoomIdentity: RoomIdentity{"closed", "instance"}, ServiceEpoch: "runtime"}} {
		r := updateRequest(t, server.URL, "GET", updatesURL(m.epoch, info), cookie, nil)
		if r.StatusCode == 200 {
			t.Fatal("stale runtime accepted")
		}
		r.Body.Close()
	}
	if len(m.runtimes) != 1 {
		t.Fatal("subscription assembled a runtime")
	}
	resp := updateRequest(t, server.URL, "GET", updatesURL(m.epoch, svc.info), cookie, nil)
	frames := updateFrames(resp.Body)
	nextUpdateFrame(t, frames)
	global := updateRequest(t, server.URL, "GET", updatesURL(m.epoch, ServiceInfo{}), cookie, nil)
	gframes := updateFrames(global.Body)
	nextUpdateFrame(t, gframes)
	svc.freeze()
	awaitUpdateClose(t, frames)
	// Reopen the same persisted Room with a new runtime epoch, keeping the
	// manager's global stream and notification center alive.
	closeService(t, svc)
	replacement, err := newService(svc.store.dir, svc.info.RoomID, serviceConfig{access: m.config.access, updates: m.updates})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeService(t, replacement) })
	m.mu.Lock()
	m.setRuntimeServiceLocked(replacement.info.RoomIdentity, replacement)
	m.mu.Unlock()
	if replacement.info.RoomIdentity != svc.info.RoomIdentity || replacement.info.ServiceEpoch == svc.info.ServiceEpoch {
		t.Fatal("wrong reopened identity")
	}
	oldResponse := updateRequest(t, server.URL, "GET", updatesURL(m.epoch, svc.info), cookie, nil)
	if oldResponse.StatusCode == 200 {
		t.Fatal("old epoch subscribed after replacement")
	}
	oldResponse.Body.Close()
	reopened := updateRequest(t, server.URL, "GET", updatesURL(m.epoch, replacement.info), cookie, nil)
	reopenedFrames := updateFrames(reopened.Body)
	nextUpdateFrame(t, reopenedFrames)
	m.updates.mark(uiUpdate{Topic: "catalog"})
	if f := nextUpdateFrame(t, gframes); f.data["topic"] != "catalog" {
		t.Fatal(f)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	awaitUpdateClose(t, gframes)
	// A catalog frame may already have been emitted to the selected stream.
	reopened.Body.Close()
	if err := server.Config.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUIUpdatesHTTPHeartbeat(t *testing.T) {
	svc, _, _, server, cookie, _ := updateHTTPService(t, false)
	resp := updateRequest(t, server.URL, "GET", updatesURL(svc.info.ServiceEpoch, svc.info), cookie, nil)
	frames := updateFrames(resp.Body)
	hello := nextUpdateFrame(t, frames)
	select {
	case f := <-frames:
		if f.event != "heartbeat" || f.data["connection_id"] != hello.data["connection_id"] {
			t.Fatal(f)
		}
	case <-time.After(uiUpdateHeartbeat + time.Second):
		t.Fatal("no heartbeat")
	}
}

// A transport whose Flush can block, while still honoring concurrent write
// deadlines like net/http. It records frames without sharing a business lock.
type blockingUpdateWriter struct {
	header  http.Header
	mu      sync.Mutex
	body    bytes.Buffer
	flushed chan struct{}
	release chan struct{}
	abort   chan struct{}
	once    sync.Once
	first   sync.Once
	timer   *time.Timer
}

func newBlockingUpdateWriter() *blockingUpdateWriter {
	return &blockingUpdateWriter{header: make(http.Header), flushed: make(chan struct{}), release: make(chan struct{}), abort: make(chan struct{})}
}
func (w *blockingUpdateWriter) Header() http.Header { return w.header }
func (w *blockingUpdateWriter) WriteHeader(int)     {}
func (w *blockingUpdateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}
func (w *blockingUpdateWriter) FlushError() error {
	w.first.Do(func() { close(w.flushed) })
	select {
	case <-w.release:
		return nil
	case <-w.abort:
		return context.DeadlineExceeded
	}
}
func (w *blockingUpdateWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	if !deadline.IsZero() {
		w.timer = time.AfterFunc(time.Until(deadline), func() { w.once.Do(func() { close(w.abort) }) })
	}
	return nil
}
func (w *blockingUpdateWriter) content() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func TestUIUpdatesRegistrationBeforeHelloAndShortRequestSlots(t *testing.T) {
	a, token, _ := accessFixture(t)
	svc, err := newService(t.TempDir(), "updates", serviceConfig{access: a})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	handler := a.wrap(svc)
	cookie := accessLogin(t, handler, token)
	writer := newBlockingUpdateWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", a.internal+updatesURL(svc.info.ServiceEpoch, svc.info), nil).WithContext(ctx)
	r.RemoteAddr = "127.0.0.1:45000"
	r.AddCookie(cookie)
	// All ordinary slots are occupied before the stream is admitted.
	for range cap(svc.requests) {
		svc.requests <- struct{}{}
	}
	done := make(chan struct{})
	go func() { handler.ServeHTTP(writer, r); close(done) }()
	select {
	case <-writer.flushed:
	case <-time.After(time.Second):
		t.Fatal("stream used short-request gate")
	}
	for range cap(svc.requests) {
		<-svc.requests
	}
	if len(svc.requests) != 0 {
		t.Fatal("stream retained an ordinary request slot")
	}
	post := PostMessage{RoomIdentity: svc.info.RoomIdentity, Caller: Caller{Author: Author{Kind: "human", ID: a.operator}}, ClientMessageID: "during-hello", Text: "saved while hello blocked"}
	fast := updateSub(t, svc.updates, 100, a.operator, svc.info)
	if _, err := svc.room.Call[PostReceipt](ctx, post); err != nil {
		t.Fatal(err)
	}
	if got, _ := fast.take(); len(got) != 4 {
		t.Fatal("blocked connection delayed other observers", got)
	}
	if w := accessRequest(handler, "GET", "/v1/info", token, "", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	close(writer.release)
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(writer.content(), `"topic":"messages"`) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop")
	}
	content := writer.content()
	if !strings.HasPrefix(content, "event: hello\n") || !strings.Contains(content, `"topic":"messages"`) || strings.Contains(content, post.Text) {
		t.Fatal(content)
	}
}

func TestUIUpdatesBlockedWriterCancellation(t *testing.T) {
	for _, reason := range []string{"request", "logout", "settings", "shutdown", "expiry", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			a, token, _ := accessFixture(t)
			a.document = newSystemSettings(a.config).configDocument
			svc, err := newService(t.TempDir(), "updates", serviceConfig{access: a})
			if err != nil {
				t.Fatal(err)
			}
			defer closeService(t, svc)
			handler := a.wrap(svc)
			cookie := accessLogin(t, handler, token)
			if reason == "expiry" {
				a.mu.Lock()
				s := a.sessions[sha256.Sum256([]byte(cookie.Value))]
				s.expires = time.Now().Add(250 * time.Millisecond)
				a.sessions[sha256.Sum256([]byte(cookie.Value))] = s
				a.mu.Unlock()
			}
			writer := newBlockingUpdateWriter()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest("GET", a.internal+updatesURL(svc.info.ServiceEpoch, svc.info), nil).WithContext(ctx)
			r.RemoteAddr = "127.0.0.1:45000"
			r.AddCookie(cookie)
			done := make(chan struct{})
			go func() { handler.ServeHTTP(writer, r); close(done) }()
			select {
			case <-writer.flushed:
			case <-time.After(time.Second):
				t.Fatal("hello not reached")
			}
			switch reason {
			case "request":
				cancel()
			case "logout":
				if w := accessRequest(handler, "POST", "/v1/access/logout", "", a.internal, "{}", cookie); w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			case "settings":
				if w := accessRequest(handler, "POST", "/v1/access/settings", token, "", settingsBody(a, true, false, "Newpassword-987654321!", token, ""), nil); w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			case "shutdown":
				svc.freeze()
			case "expiry":
			case "deadline":
			}
			wait := time.Second
			if reason == "deadline" {
				wait = uiUpdateWriteTimeout + time.Second
			}
			select {
			case <-done:
			case <-time.After(wait):
				t.Fatal("blocked Flush was not interrupted", reason)
			}
		})
	}
}

func TestUIUpdatesHTTPConnectionLimit(t *testing.T) {
	svc, _, _, server, cookie, _ := updateHTTPService(t, false)
	path := updatesURL(svc.info.ServiceEpoch, ServiceInfo{})
	for range uiUpdateMaxSessionConnections {
		resp := updateRequest(t, server.URL, "GET", path, cookie, nil)
		if resp.StatusCode != 200 {
			t.Fatal(resp.Status)
		}
		nextUpdateFrame(t, updateFrames(resp.Body))
	}
	resp := updateRequest(t, server.URL, "GET", path, cookie, nil)
	if resp.StatusCode == 200 {
		t.Fatal("ninth browser connection accepted")
	}
	if len(svc.requests) != 0 {
		t.Fatal("long connections used ordinary request slots")
	}
}

func TestUIUpdatesAnonymousAccessChangesCancelStreams(t *testing.T) {
	a, _ := anonymousAccessFixture(t)
	svc, err := newService(t.TempDir(), "updates", serviceConfig{access: a})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	handler := a.wrap(svc)
	writer := newBlockingUpdateWriter()
	r := httptest.NewRequest("GET", a.internal+updatesURL(svc.info.ServiceEpoch, ServiceInfo{}), nil)
	r.RemoteAddr = "127.0.0.1:45000"
	done := make(chan struct{})
	go func() { handler.ServeHTTP(writer, r); close(done) }()
	select {
	case <-writer.flushed:
	case <-time.After(time.Second):
		t.Fatal("anonymous local stream not admitted")
	}
	w := accessRequest(handler, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, true, false, "Newpassword-987654321!", "", ""), nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enabling login did not cancel anonymous stream")
	}
}

func TestUIUpdatesSessionRevocationIsolation(t *testing.T) {
	a, token, h := accessFixture(t)
	first := accessLogin(t, h, token)
	second := accessLogin(t, h, token)
	lease := func(cookie *http.Cookie) *uiUpdateAccessLease {
		r := httptest.NewRequest("GET", a.internal+"/v1/updates", nil)
		r.AddCookie(cookie)
		r = r.WithContext(context.WithValue(r.Context(), accessContextKey{}, a))
		l, err := a.watchUIUpdates(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.unwatchUIUpdates(l) })
		return l
	}
	one, two := lease(first), lease(second)
	a.mu.Lock()
	sessions := maps.Clone(a.sessions)
	delete(sessions, one.session.hash)
	err := a.replaceSessions(sessions)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-one.done:
	default:
		t.Fatal("revoked session remained subscribed")
	}
	select {
	case <-two.done:
		t.Fatal("unrelated session revoked")
	default:
	}
}
