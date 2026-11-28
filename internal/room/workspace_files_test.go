package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fileRootFixture(t *testing.T) (*workspaceFileRoot, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := root.Stat(".")
	f := &workspaceFileRoot{root: root, info: info, directory: dir}
	t.Cleanup(f.Close)
	return f, dir
}
func writeFileFixture(t *testing.T, name string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestWorkspaceFilePreviewAndPathBoundaries(t *testing.T) {
	f, dir := fileRootFixture(t)
	for name, body := range map[string][]byte{"docs/中文 空格.md": []byte("# Hello\n\n|a|b|\n|-|-|\n|1|2|"), "note.txt": []byte("hello"), ".hidden": []byte("hidden"), "invalid.txt": {0xff}, "nul.txt": {'a', 0, 'b'}, "fake.html": []byte("# Download only"), "image.svg": []byte("<svg/>"), "big.md": bytes.Repeat([]byte("x"), workspaceFileTextBytes+1), ".teamx/auth/token": []byte("SECRET"), ".git/config": []byte("SECRET")} {
		writeFileFixture(t, filepath.Join(dir, name), body)
	}
	if err := os.Symlink("docs/中文 空格.md", filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".teamx/auth", filepath.Join(dir, "protected-link")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeFileFixture(t, filepath.Join(outside, "secret"), []byte("SECRET"))
	if err := os.Symlink(outside, filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../secret", "docs/../../secret", "/etc/passwd", "docs\\secret", "a\nfile", "file:secret", "outside/secret", "protected-link/token", ".teamx/auth/token", ".git/config", strings.Repeat("a", 4097)} {
		if _, err := f.preview(t.Context(), p); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	for _, p := range []string{"inside", "docs/中文 空格.md"} {
		preview, err := f.preview(t.Context(), p)
		if err != nil || preview.Text == "" {
			t.Fatal(p, preview, err)
		}
	}
	for _, p := range []string{"invalid.txt", "nul.txt", "fake.html", "image.svg", "big.md"} {
		preview, err := f.preview(t.Context(), p)
		if err != nil || preview.PreviewKind != "unsupported" || preview.Text != "" || !preview.DownloadAvailable {
			t.Fatal(p, preview, err)
		}
	}
	hidden, err := f.preview(t.Context(), ".hidden")
	if err != nil || hidden.Text != "hidden" {
		t.Fatal(hidden, err)
	}
	entries, err := f.list(t.Context(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries.Entries {
		if strings.HasPrefix(entry.Name, ".") || entry.Name == "protected-link" || entry.Name == "outside" {
			t.Fatal(entries)
		}
	}
	entries, err = f.list(t.Context(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries.Entries {
		if entry.Name == ".teamx" || entry.Name == ".git" {
			t.Fatal(entries)
		}
	}
	if entries.Entries[0].Kind != "directory" {
		t.Fatal("directories must sort first", entries)
	}
}
func TestWorkspaceFileBoundedDirectoryAndChanges(t *testing.T) {
	f, dir := fileRootFixture(t)
	for i := 0; i < workspaceFileScanLimit+1; i++ {
		writeFileFixture(t, filepath.Join(dir, fmt.Sprintf("f-%05d", i)), nil)
	}
	listing, err := f.list(t.Context(), "", false)
	if err != nil || !listing.Truncated || len(listing.Entries) != workspaceFileEntryLimit {
		t.Fatal(len(listing.Entries), listing.Truncated, err)
	}
	b, _ := json.Marshal(listing)
	if len(b) > workspaceFileJSONBytes {
		t.Fatal(len(b))
	}
	before, _ := f.open("f-00000")
	info, _ := before.Stat()
	before.Close()
	writeFileFixture(t, filepath.Join(dir, "new"), []byte("replacement"))
	if err := os.Rename(filepath.Join(dir, "new"), filepath.Join(dir, "f-00000")); err != nil {
		t.Fatal(err)
	}
	if err := f.unchanged("f-00000", info); err == nil {
		t.Fatal("replacement admitted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.list(ctx, "", false); err == nil {
		t.Fatal("ignored cancellation")
	}
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir + "-old") })
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.unchanged("f-00001", info); err == nil {
		t.Fatal("replaced root admitted")
	}
}

func TestWorkspaceFilesHTTPCurrentSourceAndIsolation(t *testing.T) {
	t.Setenv("USER", humanCaller.ID)
	m, external := directoryTestManager(t)
	room := managerCreate(t, m, "files-http")
	m.workspaceProtected = append(m.workspaceProtected, filepath.Dir(m.store.dir))
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	directory := filepath.Join(m.roomDir(room.RoomID), member.Workspace)
	writeFileFixture(t, filepath.Join(directory, "docs/read.md"), []byte("# Default"))
	writeFileFixture(t, filepath.Join(external, "read.md"), []byte("# External"))
	q := url.Values{"room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}}
	get := func(endpoint string) *httptest.ResponseRecorder { return managerHTTP(m, endpoint+"?"+q.Encode(), nil) }
	getSources := func() []workspaceFileSource {
		response := get("/v1/workspace/files/sources")
		var result struct {
			Sources []workspaceFileSource `json:"sources"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 {
			t.Fatal(response.Code, response.Body.String(), err)
		}
		if response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(response.Header())
		}
		return result.Sources
	}
	sources := getSources()
	if len(sources) != len(saved.Members) || !sources[0].Available || len(sources[0].RootID) != 68 {
		t.Fatal(sources)
	}
	rootID := sources[0].RootID
	q.Set("membership_id", member.MembershipID)
	q.Set("root_id", rootID)
	q.Set("path", "")
	rootListing := get("/v1/workspace/files")
	var listing map[string]any
	if err := json.Unmarshal(rootListing.Body.Bytes(), &listing); err != nil || rootListing.Code != 200 || listing["path"] != "" {
		t.Fatal(rootListing.Code, rootListing.Body.String(), err)
	}
	q.Del("membership_id")
	q.Del("root_id")
	q.Del("path")
	if rootID != workspaceRootID(room.RoomIdentity, member.MembershipID, filepath.Clean(directory)) {
		real, _ := filepath.EvalSymlinks(directory)
		if rootID != workspaceRootID(room.RoomIdentity, member.MembershipID, real) {
			t.Fatal(rootID, real)
		}
	}
	q.Set("membership_id", member.MembershipID)
	q.Set("root_id", rootID)
	q.Set("path", "docs/read.md")
	response := get("/v1/workspace/files/preview")
	var preview workspaceFilePreview
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil || response.Code != 200 || preview.Text != "# Default" {
		t.Fatal(response.Code, response.Body.String(), err)
	}
	// Browsing unopened Rooms does not create a runtime or mutate snapshots.
	if len(m.runtimes) != 0 {
		t.Fatal("file browsing started an Agent", len(m.runtimes))
	}
	q.Set("message_seq", "1")
	if r := get("/v1/workspace/files/preview"); r.Code != 400 {
		t.Fatal("mixed selector", r.Code)
	}
	q.Del("message_seq")
	q.Set("caller_id", "someone-else")
	if r := get("/v1/workspace/files/preview"); r.Code != 403 {
		t.Fatal(r.Code)
	}
	q.Set("caller_id", humanCaller.ID)
	q.Set("root_id", "forged")
	if r := get("/v1/workspace/files/preview"); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	q.Set("root_id", rootID)
	// Unrelated member changes do not alter this source's fingerprint.
	m.workspaceProtected = nil
	saved.Workspace.MemberDirectories = map[string]string{saved.Members[1].MembershipID: external}
	configPath := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
	if err := m.store.save(configPath, saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if r := get("/v1/workspace/files/preview"); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	saved.Workspace.MemberDirectories[member.MembershipID] = external
	if err := m.store.save(configPath, saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if r := get("/v1/workspace/files/preview"); r.Code != 409 {
		t.Fatal("old source followed new root", r.Code, r.Body.String())
	}
	q.Del("membership_id")
	q.Del("root_id")
	q.Del("path")
	sources = getSources()
	if sources[0].RootID == rootID || sources[0].SharedKey != sources[1].SharedKey {
		t.Fatal(sources)
	}
	q.Set("membership_id", member.MembershipID)
	q.Set("root_id", sources[0].RootID)
	q.Set("path", "read.md")
	if r := get("/v1/workspace/files/preview"); r.Code != 200 || !strings.Contains(r.Body.String(), "# External") {
		t.Fatal(r.Code, r.Body.String())
	}
	q.Set("room_instance", "other")
	if r := get("/v1/workspace/files/preview"); r.Code != 403 {
		t.Fatal(r.Code)
	}
	q.Set("room_instance", room.RoomInstance)
	// Same path, stable identity after a normal epoch change; old epochs reject.
	oldEpoch := m.epoch
	m.epoch = "restarted-epoch"
	if r := get("/v1/workspace/files/preview"); r.Code != 403 {
		t.Fatal(r.Code)
	}
	q.Set("service_epoch", m.epoch)
	if r := get("/v1/workspace/files/preview"); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	m.epoch = oldEpoch
}

func TestPublicWorkspaceFilesExcludeNewSpaceRootsAndAliases(t *testing.T) {
	t.Setenv("USER", humanCaller.ID)
	m, project := directoryTestManager(t)
	room := managerCreate(t, m, "public-files")
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	saved.Workspace.MemberDirectories = map[string]string{member.MembershipID: project}
	if err := m.store.save(filepath.Join(m.roomDir(room.RoomID), "room-config.json"), saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	claims := &spaceDirectoryClaims{sharedExternal: true, directory: spaceDirectory{Spaces: []SpaceRecord{{ID: "component-fixture", Roots: spaceRoots{Data: m.store.dir, Teams: m.teamsDir}}}}}
	m.config.directoryClaims = claims
	m.config.access = &serverAccess{
		space:    SpaceContext{SpaceID: "component-fixture", OwnerUserID: humanCaller.ID, RuntimeEpoch: "component-fixture"},
		identity: &personalRegistration{User: User{ID: humanCaller.ID, Status: "active", AuthVersion: 1}, Space: SpaceRecord{ID: "component-fixture", OwnerUserID: humanCaller.ID, Status: "active"}},
	}
	if err := os.MkdirAll(filepath.Join(m.roomDir(room.RoomID), saved.Members[1].Workspace), 0700); err != nil {
		t.Fatal(err)
	}
	writeFileFixture(t, filepath.Join(project, "read.md"), []byte("public"))
	// A non-dot directory demonstrates that protection comes from the registry,
	// rather than the generic .teamx/hidden-file exclusions.
	private := filepath.Join(project, "other-state")
	writeFileFixture(t, filepath.Join(private, "secret.txt"), []byte("private bytes"))
	if err := os.Symlink(private, filepath.Join(project, "alias")); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}}
	request := func(endpoint string) *httptest.ResponseRecorder { return managerHTTP(m, endpoint+"?"+q.Encode(), nil) }
	var sources struct {
		Sources []workspaceFileSource `json:"sources"`
	}
	r := request("/v1/workspace/files/sources")
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &sources) != nil || len(sources.Sources) < 2 || !sources.Sources[0].Available || !sources.Sources[1].Available {
		t.Fatal(r.Code, r.Body.String())
	}
	q.Set("membership_id", member.MembershipID)
	q.Set("root_id", sources.Sources[0].RootID)
	// The manager and source identity predate this Space registration.
	claims.mu.Lock()
	claims.directory.Spaces = append(claims.directory.Spaces, SpaceRecord{ID: "other", Roots: spaceRoots{Home: private}})
	claims.mu.Unlock()
	q.Set("path", "")
	q.Set("show_hidden", "true")
	r = request("/v1/workspace/files")
	var listing workspaceFileListing
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &listing) != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "read.md" {
		t.Fatal(r.Code, r.Body.String())
	}
	q.Del("show_hidden")
	for _, path := range []string{"other-state/secret.txt", "alias/secret.txt"} {
		q.Set("path", path)
		for _, endpoint := range []string{"/v1/workspace/files/preview", "/v1/workspace/files/content"} {
			r := request(endpoint)
			if r.Code == 200 || strings.Contains(r.Body.String(), "private bytes") {
				t.Fatal("private bytes delivered", endpoint, path, r.Code)
			}
		}
		q.Set("download", "true")
		if r := request("/v1/workspace/files/content"); r.Code == 200 {
			t.Fatal("private download admitted", path)
		}
		q.Del("download")
	}
	q.Set("path", "read.md")
	if r := request("/v1/workspace/files/preview"); r.Code != 200 || !strings.Contains(r.Body.String(), "public") {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestWorkspaceFilesHumanAccessAndCancellation(t *testing.T) {
	a, token, h := accessFixture(t)
	execution, revoke, err := a.issueExecution(executionBinding{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, ServiceEpoch: "epoch"}, Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	for _, endpoint := range []string{"/v1/workspace/files", "/v1/workspace/files/sources", "/v1/workspace/files/preview", "/v1/workspace/files/content"} {
		for _, credential := range []string{"", execution} {
			if r := accessRequest(h, "GET", endpoint, credential, "", "", nil); r.Code == 200 {
				t.Fatal(endpoint, r.Code)
			}
		}
		if r := accessRequest(h, "GET", endpoint, token, "http://evil.invalid", "", nil); r.Code != 403 {
			t.Fatal(r.Code)
		}
	}
	// A cancelled queue wait frees all capacity without entering the manager.
	for i := 0; i < cap(workspaceFileReads); i++ {
		workspaceFileReads <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(workspaceFileReads); i++ {
			<-workspaceFileReads
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/workspace/files/sources?room_id=r&room_instance=i&service_epoch=e&caller_id=h", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); (&roomManager{}).serveWorkspaceFiles(httptest.NewRecorder(), req) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled reader stuck in queue")
	}
}
