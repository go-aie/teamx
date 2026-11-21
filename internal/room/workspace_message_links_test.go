package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestMessageWorkspaceSourcePersistenceAndRetry(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		t.Run(fmt.Sprint(segmented), func(t *testing.T) {
			state := historyRoom(0, "")
			if segmented {
				state.InputLog = &roomLogRoot{Version: 1}
			}
			h := newRoomHarness(t, state, nil)
			post := h.post(agentCaller("A", "generation-A"), "file-link", "[Preview](docs/result.md)")
			post.WorkspaceRootID = workspaceRootID(h.id, "generation-A", "/old/workspace")
			first := roomCall[PostReceipt](t, h, post)
			post.WorkspaceRootID = workspaceRootID(h.id, "generation-A", "/new/workspace")
			if retry := roomCall[PostReceipt](t, h, post); retry != first {
				t.Fatal(retry, first)
			}
			post.Text = "different"
			roomError[PostReceipt](t, h, post, ErrConflict)
			h.reload(t, true)
			saved, err := h.store.LoadRoom()
			want := workspaceRootID(h.id, "generation-A", "/old/workspace")
			if err != nil || len(saved.Messages) != 1 || saved.Messages[0].WorkspaceRootID != want {
				t.Fatal(saved, err)
			}
			if roomMessageBytes(saved.Messages[0]) != len(saved.Messages[0].Text)+len(want) {
				t.Fatal("root missing from byte budget")
			}
			business, _ := json.Marshal(projectRoomMessage(saved.Messages[0]))
			if strings.Contains(string(business), want) || strings.Contains(string(business), "workspace_root_id") {
				t.Fatal("technical source leaked to Agent DTO")
			}
			if segmented {
				saved.Messages[0].WorkspaceRootID = post.WorkspaceRootID
				if err := h.store.SaveRoom(saved); err == nil {
					t.Fatal("rewrote immutable logged message")
				}
			}
		})
	}
}

func TestMessageWorkspaceSourceAtomicFailureAndValidation(t *testing.T) {
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	root := workspaceRootID(h.id, "generation-A", "/workspace")
	post := h.post(agentCaller("A", "generation-A"), "atomic-file", "[Preview](result.md)")
	post.WorkspaceRootID = root
	fault := errors.New("file-link commit failure")
	real := h.store.ops.createTemp
	h.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, fault }
	roomError[PostReceipt](t, h, post, fault)
	saved, err := h.store.LoadRoom()
	if err != nil || len(saved.Messages) != 0 {
		t.Fatal("failed commit published part of message", saved, err)
	}
	h.store.ops.createTemp = real
	roomCall[PostReceipt](t, h, post)
	for _, mutate := range []func(*PostMessage){
		func(p *PostMessage) { p.WorkspaceRootID = "wr1_BAD" },
		func(p *PostMessage) { p.Caller = humanCaller },
		func(p *PostMessage) { p.FollowupNotice = true },
		func(p *PostMessage) { p.DurablePublication = true },
	} {
		invalid := post
		invalid.ClientMessageID = "invalid"
		mutate(&invalid)
		roomError[PostReceipt](t, h, invalid, ErrRequest)
	}
	// A missing source is compatible with old/native messages and does not block speech.
	post.ClientMessageID = "without-source"
	post.WorkspaceRootID = ""
	roomCall[PostReceipt](t, h, post)
	saved, _ = h.store.LoadRoom()
	for _, mutate := range []func(*RoomMessage){
		func(m *RoomMessage) { m.WorkspaceRootID = "wr1_" + strings.Repeat("A", 64) },
		func(m *RoomMessage) { m.Author = humanCaller.Author; m.AuthorMembershipID = "" },
		func(m *RoomMessage) { m.AuthorMembershipID = "" },
	} {
		invalid := (&Room{state: saved}).candidate()
		mutate(&invalid.Messages[0])
		if err := invalid.validate(); err == nil {
			t.Fatal("accepted invalid stored workspace source")
		}
	}
	full := historyRoom(maxHistoryBytes/maxTextBytes, strings.Repeat("x", maxTextBytes))
	full.Messages[0].Text = full.Messages[0].Text[:maxTextBytes-68]
	r := &Room{state: full}
	post.RoomIdentity = full.RoomIdentity
	post.WorkspaceRootID = root
	post.Text = "x"
	post.ClientMessageID = "root-exceeds-capacity"
	if err := r.Post(nil, post, actorx.Reply[PostReceipt]{}); !errors.Is(err, ErrCapacity) {
		t.Fatal("source bypassed capacity", err)
	}
}

// Exercise the production Session host, execution credential authentication and
// send adapter; only the backend subprocess/model component is controlled.
func TestMessageWorkspaceSourceExecutionCredentialChain(t *testing.T) {
	a, _, _ := accessFixture(t)
	dir, id := recoveryFixture(t)
	svc, err := newService(dir, id.RoomID, recoveryConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	opts := adapterOptions(svc.store, id)
	for i, value := range opts.CLIEnv {
		if strings.HasPrefix(value, "TEAMX_SERVICE_EPOCH=") {
			opts.CLIEnv[i] = "TEAMX_SERVICE_EPOCH=" + svc.info.ServiceEpoch
		}
	}
	credentials, err := newModelCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := testModelTarget(t, id, credentials)
	originalDirectory := target.Profile.Workspace
	alias := filepath.Join(t.TempDir(), "cwd-alias")
	if err := os.Symlink(originalDirectory, alias); err != nil {
		t.Fatal(err)
	}
	target.Profile.Workspace = alias
	var token string
	backend := dshBackend{store: svc.store, access: a, credentials: credentials, model: &target, profile: target.Profile, open: func(_ context.Context, o dsh.Options) (dshComponent, error) {
		token = o.ExecutionToken
		return &adapterComponent{}, nil
	}}
	session, err := backend.OpenSession(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(target.Profile.Workspace)
	want := workspaceRootID(id.RoomIdentity, id.MembershipID, canonical)
	if got := a.executions[sha256.Sum256([]byte(token))].WorkspaceRootID; got != want {
		t.Fatal(got, want)
	}
	handler := a.wrap(svc)
	send := func(key string, extra bool) int {
		body, _ := json.Marshal(apiRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), ClientMessageID: key, Text: "[Preview](result.md)"})
		if extra {
			body = append(body[:len(body)-1], []byte(`,"workspace_root_id":"wr1_`+strings.Repeat("a", 64)+`"}`)...)
		}
		return accessRequest(handler, "POST", "/v1/send", token, "", string(body), nil).Code
	}
	if code := send("forged", true); code != 400 {
		t.Fatal("accepted externally writable source", code)
	}
	if code := send("frozen", false); code != 200 {
		t.Fatal(code)
	}
	if err := session.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	if code := send("revoked", false); code != 401 {
		t.Fatal(code)
	}
	// Recovery retains the frozen source even if the alias target and current
	// profile have changed. It issues a new token with the same original source.
	backend.recover = true
	backend.profile.Workspace = t.TempDir()
	session, err = backend.OpenSession(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(t.Context())
	if got := a.executions[sha256.Sum256([]byte(token))].WorkspaceRootID; got != want {
		t.Fatal("recovery rebound current directory", got, want)
	}
	if code := send("frozen", false); code != 200 {
		t.Fatal(code)
	}
	if code := send("recovered", false); code != 200 {
		t.Fatal(code)
	}
	room, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	if len(room.Messages) != 5 || room.Messages[3].WorkspaceRootID != want || room.Messages[4].WorkspaceRootID != want {
		t.Fatal("send lost trusted binding", room.Messages)
	}
	// No source in a valid execution binding still allows text publication.
	plain, revoke, err := a.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	token = plain
	if code := send("no-source", false); code != 200 {
		t.Fatal(code)
	}
	// A later new generation must observe its own cwd rather than inherit the
	// previous generation's hint through a cloned model selection.
	next := backend
	snapshot := target.clone()
	snapshot.WorkspaceRootID = want
	next.model = &snapshot
	if err := next.saveModelSnapshot(opts, sessionReference{Directory: sessionGenerationForOperation(strings.Repeat("a", 32))}); err != nil {
		t.Fatal(err)
	}
	newDirectory, _ := filepath.EvalSymlinks(alias)
	if next.model.WorkspaceRootID != workspaceRootID(id.RoomIdentity, id.MembershipID, newDirectory) || next.model.WorkspaceRootID == want {
		t.Fatal("new generation inherited an old cwd", next.model.WorkspaceRootID)
	}
}

func TestMessageWorkspaceSourceMissingRecoveryEvidence(t *testing.T) {
	a, _, _ := accessFixture(t)
	dir, id := recoveryFixture(t)
	store, err := OpenStore(dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := adapterOptions(store, id)
	var token string
	backend := dshBackend{store: store, access: a, profile: dshProfile{Workspace: t.TempDir()}, open: func(_ context.Context, o dsh.Options) (dshComponent, error) {
		token = o.ExecutionToken
		return &adapterComponent{}, nil
	}}
	session, err := backend.OpenSession(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.executions[sha256.Sum256([]byte(token))].WorkspaceRootID == "" {
		t.Fatal("fresh execution lost its source")
	}
	if err := session.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	backend.recover = true
	backend.profile.Workspace = t.TempDir()
	session, err = backend.OpenSession(t.Context(), opts)
	if err != nil {
		t.Fatal("file hint blocked legacy recovery", err)
	}
	defer session.Close(t.Context())
	if a.executions[sha256.Sum256([]byte(token))].WorkspaceRootID != "" {
		t.Fatal("legacy recovery borrowed current directory")
	}
}

func TestWorkspaceFilesMessageSourceVisibilityAndCurrentDirectory(t *testing.T) {
	t.Setenv("USER", humanCaller.ID)
	m, external := directoryTestManager(t)
	room := managerCreate(t, m, "message-files")
	saved, state := readManagedRoom(t, m, room)
	member := saved.Members[0]
	directory, _ := filepath.EvalSymlinks(filepath.Join(m.roomDir(room.RoomID), member.Workspace))
	writeFileFixture(t, filepath.Join(directory, "docs/result.md"), []byte("# Original"))
	writeFileFixture(t, filepath.Join(directory, "not-in-message.md"), []byte("# Same authorized root"))
	writeFileFixture(t, filepath.Join(external, "docs/result.md"), []byte("# Different source"))
	state.Messages = []RoomMessage{
		{Seq: 1, ClientMessageID: "link", Author: Author{"agent", member.AgentID}, AuthorMembershipID: member.MembershipID, WorkspaceRootID: workspaceRootID(room.RoomIdentity, member.MembershipID, directory), Text: "[Preview](docs/result.md)", CreatedAt: time.Now().UTC()},
		{Seq: 2, ClientMessageID: "legacy", Author: Author{"agent", member.AgentID}, AuthorMembershipID: member.MembershipID, Text: "[Old](docs/result.md)", CreatedAt: time.Now().UTC()},
		{Seq: 3, ClientMessageID: "human", Author: humanCaller.Author, Text: "[Human](docs/result.md)", CreatedAt: time.Now().UTC()},
	}
	state.LatestSeq = 3
	state.Revision += 3
	store, err := OpenStore(m.roomDir(room.RoomID), room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "message_seq": {"1"}, "path": {"docs/result.md"}}
	get := func(want int, marker string) {
		t.Helper()
		w := managerHTTP(m, "/v1/workspace/files/preview?"+q.Encode(), nil)
		if w.Code != want || !strings.Contains(w.Body.String(), marker) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	get(200, "# Original")
	// Published links survive a normal Store reopen and a new service epoch.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(m.roomDir(room.RoomID), room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m.epoch = "restarted-files-service"
	q.Set("service_epoch", m.epoch)
	get(200, "# Original")
	other := managerCreate(t, m, "other-message-files")
	q.Set("room_id", other.RoomID)
	q.Set("room_instance", other.RoomInstance)
	get(404, "reference_unavailable")
	q.Set("room_id", room.RoomID)
	q.Set("room_instance", room.RoomInstance)
	q.Set("path", "not-in-message.md")
	get(200, "# Same authorized root") // no backend Markdown matching
	q.Set("path", "../secret")
	get(400, "request")
	q.Set("path", "docs/result.md")
	q.Set("membership_id", saved.Members[1].MembershipID)
	get(400, "request")
	q.Del("membership_id")
	for _, seq := range []string{"2", "3", "99"} {
		q.Set("message_seq", seq)
		get(404, "reference_unavailable")
	}
	q.Set("message_seq", "1")
	removed := (&Room{state: state}).candidate()
	removed.Members = removed.Members[1:]
	if err := store.SaveRoom(removed); err != nil {
		t.Fatal(err)
	}
	get(409, "workspace_source_changed")
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	saved.Workspace.MemberDirectories = map[string]string{member.MembershipID: external}
	configPath := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
	if err := m.store.save(configPath, saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	get(409, "workspace_source_changed")
	saved.Workspace.MemberDirectories = nil
	if err := m.store.save(configPath, saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	get(200, "# Original")
	writeFileFixture(t, filepath.Join(directory, "docs/result.md"), []byte("# Updated"))
	get(200, "# Updated")
	state.ClearedThrough = 3
	state.Messages = []RoomMessage{}
	state.Revision++
	for i := range state.Members {
		state.Members[i].ReadThrough = 3
	}
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	get(404, "reference_unavailable")
	q.Del("message_seq")
	q.Set("membership_id", member.MembershipID)
	q.Set("root_id", workspaceRootID(room.RoomIdentity, member.MembershipID, directory))
	get(200, "# Updated")
	if len(m.runtimes) != 0 {
		t.Fatal("file reads started a runtime")
	}
	if _, err := os.Stat(filepath.Join(directory, "docs/result.md")); err != nil {
		t.Fatal("clear removed workspace file", err)
	}
}
