//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func humanReadHTTP(t *testing.T, handler http.Handler, path string, req any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data)))
	return w
}

func decodeHumanRead(t *testing.T, w *httptest.ResponseRecorder) humanReadState {
	t.Helper()
	var v humanReadState
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return v
}

func TestHumanReadHTTPIndependentClientsRestartAndClear(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	room := historyRoom(4, "@"+humanOperator()+" 请确认")
	room.Members = []MemberState{}
	for i := range room.Messages {
		room.Messages[i].Author = Author{Kind: "agent", ID: "assistant"}
	}
	room.Messages[0].Author = Author{Kind: "human", ID: humanOperator()}
	room.Messages[1].Text = "普通回复"
	if err := store.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	svc, err := newService(dir, room.RoomID, serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { closeService(t, svc) }()
	server := httptest.NewServer(svc)
	defer func() { server.Close() }()
	// Separate transports model independently connected devices. Each sends
	// only its own increment, with no shared browser or local storage state.
	one, two := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}, &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	defer one.CloseIdleConnections()
	defer two.CloseIdleConnections()
	caller := Caller{Author: Author{Kind: "human", ID: humanOperator()}}
	request := func(client *http.Client, path string, req any) humanReadState {
		t.Helper()
		data, _ := json.Marshal(req)
		res, err := client.Post(server.URL+path, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v humanReadState
		if res.StatusCode != 200 {
			body, _ := io.ReadAll(res.Body)
			t.Fatal(res.StatusCode, string(body))
		}
		if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	stateReq := humanReadRequest{ServiceInfo: svc.info, Caller: caller}
	v := request(one, "/v1/human-read/state", stateReq)
	if v.Revision != 0 || v.UnreadCount != 3 || v.MentionCount != 2 {
		t.Fatal(v)
	}
	confirm := func(client *http.Client, seqs ...uint64) humanReadState {
		t.Helper()
		latest := uint64(4)
		return request(client, "/v1/human-read/confirm", humanReadRequest{ServiceInfo: svc.info, Caller: caller, SnapshotSeq: &latest, SeenSeqs: &seqs})
	}
	confirm(one, 4)
	v = confirm(two, 2, 2)
	if !reflect.DeepEqual(v.SeenSeqs, []uint64{2, 4}) || v.UnreadCount != 1 || v.MentionCount != 1 {
		t.Fatal(v)
	}
	v = request(one, "/v1/human-read/state", stateReq)
	if v.Revision != 2 || !reflect.DeepEqual(v.SeenSeqs, []uint64{2, 4}) {
		t.Fatal(v)
	}
	server.Close()
	closeService(t, svc)
	svc, err = newService(dir, room.RoomID, serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(svc)
	if w := humanReadHTTP(t, svc, "/v1/human-read/state", stateReq); w.Code != 403 {
		t.Fatal("old epoch accepted", w.Code)
	}
	stateReq.ServiceInfo = svc.info
	v = request(two, "/v1/human-read/state", stateReq)
	if v.Revision != 2 || !reflect.DeepEqual(v.SeenSeqs, []uint64{2, 4}) {
		t.Fatal(v)
	}
	through := uint64(3)
	w := humanReadHTTP(t, svc, "/v1/messages/clear", apiRequest{ServiceInfo: svc.info, Caller: caller, SnapshotSeq: &through})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	v = request(one, "/v1/human-read/state", stateReq)
	if v.ClearedThrough != 3 || !reflect.DeepEqual(v.SeenSeqs, []uint64{4}) || v.UnreadCount != 0 {
		t.Fatal(v)
	}
	// A clear racing with a stale observation cannot mark the next arrival.
	w = humanReadHTTP(t, svc, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: "other"}}, ClientMessageID: "new", Text: "new reply"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	v = confirm(two, 3)
	if v.SnapshotSeq != 5 || v.UnreadCount != 1 || !reflect.DeepEqual(v.SeenSeqs, []uint64{4}) {
		t.Fatal(v)
	}
}

func TestHumanReadHTTPIdentityLimitsAndFaultIsolation(t *testing.T) {
	a, admin, _ := accessFixture(t)
	dir := t.TempDir()
	svc, err := newService(dir, "read-api", serviceConfig{access: a})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	handler := a.wrap(svc)
	caller := Caller{Author: Author{Kind: "human", ID: a.operator}}
	base := humanReadRequest{ServiceInfo: svc.info, Caller: caller}
	post := func(path string, req any, token string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(req)
		return accessRequest(handler, "POST", path, token, "", string(data), nil)
	}
	for _, path := range []string{"/v1/human-read/state", "/v1/human-read/confirm"} {
		for _, change := range []func(*humanReadRequest){func(r *humanReadRequest) { r.RoomID = "other" }, func(r *humanReadRequest) { r.RoomInstance = "old" }, func(r *humanReadRequest) { r.ServiceEpoch = "old" }, func(r *humanReadRequest) { r.Caller.ID = "other" }, func(r *humanReadRequest) { r.Caller.Kind = "agent" }, func(r *humanReadRequest) { r.Caller.MembershipID = "old" }} {
			r := base
			change(&r)
			if w := post(path, r, admin); w.Code != 403 {
				t.Fatal(path, w.Code, w.Body.String())
			}
		}
	}
	binding := executionBinding{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "agent", ID: "A"}, MembershipID: "generation-A"}}
	token, revoke, err := a.issueExecution(binding)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	for _, path := range []string{"/v1/human-read/state", "/v1/human-read/confirm"} {
		for _, req := range []any{base, binding} {
			if w := post(path, req, token); w.Code != 403 {
				t.Fatal("execution token accepted", path, w.Code)
			}
		}
	}
	data, _ := json.Marshal(base)
	for _, body := range []string{string(data[:len(data)-1]) + `,"execution_id":1}`, string(data[:len(data)-1]) + `,"seen_seqs":[1]}`, string(data) + " {}", strings.Repeat(" ", 128<<10) + string(data)} {
		if w := accessRequest(handler, "POST", "/v1/human-read/state", admin, "", body, nil); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := post("/v1/human-read/confirm", base, admin); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, seqs := range [][]uint64{{0}, {1}, make([]uint64, 101)} {
		zero := uint64(0)
		r := base
		r.SnapshotSeq, r.SeenSeqs = &zero, &seqs
		if w := post("/v1/human-read/confirm", r, admin); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := accessRequest(handler, "POST", "/v1/human-read/state", admin, "https://unrelated.example", string(data), nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, restart := range []bool{true, false} {
		svc.restarting.Store(restart)
		w := post("/v1/human-read/state", base, admin)
		if restart && w.Code != 409 || !restart && w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// Corruption survives service loading without blocking chat or members.
	if err := os.WriteFile(svc.humanRead.path(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	closeService(t, svc)
	svc, err = newService(dir, "read-api", serviceConfig{access: a})
	if err != nil {
		t.Fatal("bad reading file blocked startup", err)
	}
	defer closeService(t, svc)
	handler = a.wrap(svc)
	base.ServiceInfo = svc.info
	w := post("/v1/human-read/state", base, admin)
	var failure apiError
	if w.Code != 503 || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Code != "human_read_unavailable" || failure.Message != "阅读状态暂未同步" {
		t.Fatal(w.Code, w.Body.String())
	}
	for path, req := range map[string]apiRequest{"/v1/send": {ServiceInfo: svc.info, Caller: caller, ClientMessageID: "still-send", Text: "chat works"}, "/v1/member/list": {ServiceInfo: svc.info, Caller: caller}, "/v1/read": {ServiceInfo: svc.info, Caller: caller, Mode: ReadAll}} {
		if w := post(path, req, admin); w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	// A frozen/removed service never recreates its directory for a late read.
	closeService(t, svc)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if w := post("/v1/human-read/state", base, admin); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("late request recreated directory", err)
	}
}

func TestHumanReadCloseWaitsForAcceptedConfirmation(t *testing.T) {
	dir := t.TempDir()
	svc, err := newService(dir, "closing-read", serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	caller := Caller{Author: Author{Kind: "human", ID: humanOperator()}}
	w := humanReadHTTP(t, svc, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: "other"}}, ClientMessageID: "m", Text: "message"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	ops := svc.humanRead.ops
	svc.humanRead.ops.syncDir = func(dir string) error { close(entered); <-release; return ops.syncDir(dir) }
	latest, seqs := uint64(1), []uint64{1}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- humanReadHTTP(t, svc, "/v1/human-read/confirm", humanReadRequest{ServiceInfo: svc.info, Caller: caller, SnapshotSeq: &latest, SeenSeqs: &seqs})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation never entered persistence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := svc.close(ctx); err == nil {
		t.Fatal("close released ownership before accepted confirmation")
	}
	if other, err := OpenStore(dir, "closing-read"); !errors.Is(err, ErrLocked) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatal("directory ownership released early", err)
	}
	once.Do(func() { close(release) })
	select {
	case w := <-response:
		decodeHumanRead(t, w)
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation stuck")
	}
	closeService(t, svc)
}

func TestHumanReadManagedBusyDeletionAndNewInstance(t *testing.T) {
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	target := managerCreate(t, m, "shared-reading")
	svc := openRoomOK(t, m, target, "open-reading")
	caller := Caller{Author: Author{Kind: "human", ID: humanOperator()}}
	stateReq := humanReadRequest{ServiceInfo: svc.info, Caller: caller}
	v := decodeHumanRead(t, managerHTTP(m, "/v1/human-read/state", stateReq))
	if v.Revision != 0 {
		t.Fatal(v)
	}
	// Keep model turns outside this read-state lifecycle test.
	svc.grantsFrozen.Store(true)
	// Exercise the shared module via managed routing with a committed message.
	_, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: svc.info.RoomIdentity, Caller: humanCaller, ClientMessageID: "managed-read", Text: "public reply"})
	if err != nil {
		t.Fatal(err)
	}
	room, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	seqs := []uint64{room.LatestSeq}
	confirm := humanReadRequest{ServiceInfo: svc.info, Caller: caller, SnapshotSeq: &room.LatestSeq, SeenSeqs: &seqs}
	decodeHumanRead(t, managerHTTP(m, "/v1/human-read/confirm", confirm))
	for _, path := range []string{"/v1/human-read/state", "/v1/human-read/confirm"} {
		m.mu.Lock()
		m.busy = true
		m.mu.Unlock()
		w := managerHTTP(m, path, confirm)
		m.mu.Lock()
		m.busy = false
		m.mu.Unlock()
		if w.Code != http.StatusConflict {
			t.Fatal("managed busy bypass", path, w.Code, w.Body.String())
		}
	}
	beginDeletion(t, m, target)
	if remaining := awaitDeletion(t, m, target); remaining != nil {
		t.Fatal(remaining)
	}
	if _, err := os.Stat(m.roomDir(target.RoomID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if w := managerHTTP(m, "/v1/human-read/confirm", confirm); w.Code != http.StatusForbidden {
		t.Fatal("deleted identity accepted", w.Code, w.Body.String())
	}
	if _, err := os.Stat(m.roomDir(target.RoomID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("late read recreated Room", err)
	}
	replacement := managerCreate(t, m, "shared-reading")
	next := openRoomOK(t, m, replacement, "open-replacement")
	if next.info.RoomIdentity == svc.info.RoomIdentity {
		t.Fatal("reused deleted Room identity")
	}
	v = decodeHumanRead(t, managerHTTP(m, "/v1/human-read/state", humanReadRequest{ServiceInfo: next.info, Caller: caller}))
	if v.Revision != 0 || len(v.SeenSeqs) != 0 {
		t.Fatal("new instance inherited reads", v)
	}
}

func TestHumanReadCatalogDoesNotWaitForLiveReceiptLock(t *testing.T) {
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	target := managerCreate(t, m, "reading-catalog-live")
	svc := openRoomOK(t, m, target, "reading-catalog-open")
	svc.humanRead.mu.Lock()
	defer svc.humanRead.mu.Unlock()
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- managerHTTP(m, "/v1/rooms", nil) }()
	select {
	case w := <-response:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var catalog roomsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
			t.Fatal(err)
		}
		if len(catalog.Rooms) != 1 || catalog.Rooms[0].HumanRead.Status != "ready" {
			t.Fatal(catalog)
		}
	case <-time.After(time.Second):
		t.Fatal("catalog waited for live receipt mutex")
	}
}
