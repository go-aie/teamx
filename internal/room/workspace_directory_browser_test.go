package room

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceDirectoryBrowserStartsAtCurrentDirectoryOrAllowedRoot(t *testing.T) {
	m, root := directoryTestManager(t)
	room := managerCreate(t, m, "directory-start")
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	defaultDir, err := filepath.EvalSymlinks(filepath.Join(m.roomDir(room.RoomID), member.Workspace))
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	q := url.Values{"room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "agent_id": {member.AgentID}, "membership_id": {member.MembershipID}}
	configPath := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
	for _, tc := range []struct {
		name, assigned, want, reason string
		home                         bool
	}{
		{name: "default", want: defaultDir},
		{name: "home", want: defaultDir, home: true},
		{name: "assigned", assigned: project, want: project},
		{name: "missing", assigned: filepath.Join(root, "missing"), want: root, reason: "directory_not_found"},
		{name: "outside-policy", assigned: outside, want: root, reason: "directory_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.workspaceRoots = &[]string{root}
			if tc.home {
				m.workspaceRoots = nil
			}
			saved.Workspace.MemberDirectories = nil
			if tc.assigned != "" {
				saved.Workspace.MemberDirectories = map[string]string{member.MembershipID: tc.assigned}
			}
			if err := m.store.save(configPath, saved, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			response := managerHTTP(m, "/v1/workspace/directories?"+q.Encode(), nil)
			var listing workspaceDirectoryListing
			if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != 200 || listing.Path != tc.want || listing.CurrentDirectoryError != tc.reason {
				t.Fatal(response.Code, response.Body.String(), err)
			}
			after, err := os.ReadFile(configPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("browsing changed the saved directory", err)
			}
			if tc.reason != "" {
				// Explicit navigation reports the error; only opening may fall back.
				manual, _ := url.ParseQuery(q.Encode())
				manual.Set("path", tc.assigned)
				if r := managerHTTP(m, "/v1/workspace/directories?"+manual.Encode(), nil); r.Code == 200 || !strings.Contains(r.Body.String(), tc.reason) {
					t.Fatal(r.Code, r.Body.String())
				}
			}
		})
	}
}

func TestWorkspaceDirectoryBrowserManagedScopeIsReadOnlyAndMemberSpecific(t *testing.T) {
	m, root := directoryTestManager(t)
	room := managerCreate(t, m, "managed-browser")
	saved, _ := readManagedRoom(t, m, room)
	member, other := saved.Members[0], saved.Members[1]
	dir := filepath.Join(m.roomDir(room.RoomID), member.Workspace)
	child := filepath.Join(dir, "docs")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	otherDir := filepath.Join(m.roomDir(room.RoomID), other.Workspace)
	if err := os.Symlink(otherDir, filepath.Join(dir, "other-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(m.roomDir(room.RoomID), "agents"), filepath.Join(dir, "private-link")); err != nil {
		t.Fatal(err)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	realChild := filepath.Join(realDir, "docs")
	q := url.Values{"room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "agent_id": {member.AgentID}, "membership_id": {member.MembershipID}}
	get := func(path string) *httptest.ResponseRecorder {
		q.Set("path", path)
		return managerHTTP(m, "/v1/workspace/directories?"+q.Encode(), nil)
	}
	var listing workspaceDirectoryListing
	response := get("")
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != 200 || listing.Path != realDir || !listing.CanSelect || listing.DirectorySource != "managed" || listing.Parent != nil {
		t.Fatal(response.Code, response.Body.String(), err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Path != realChild {
		t.Fatal("exposed a protected link", listing.Entries)
	}
	response = get(realChild)
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != 200 || listing.CanSelect || listing.DirectorySource != "managed" || listing.Parent == nil || *listing.Parent != realDir {
		t.Fatal(response.Code, response.Body.String(), err)
	}
	for _, path := range []string{filepath.Dir(dir), otherDir, filepath.Join(m.roomDir(room.RoomID), "agents"), filepath.Join(dir, "other-link"), filepath.Join(dir, "private-link")} {
		if r := get(path); r.Code == 200 {
			t.Fatal("escaped managed member scope", path, r.Body.String())
		}
	}
	// Browsing the default does not authorize binding a protected path externally.
	if _, err := m.canonicalWorkspaceDirectory(realDir, false); err == nil {
		t.Fatal("managed browser broadened external binding policy")
	}
	if r := get(root); r.Code != 200 {
		t.Fatal("home shortcut unavailable", r.Code, r.Body.String())
	}
	m.workspaceRoots = &[]string{}
	response = get("")
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != 200 || listing.Path != realDir || !listing.CanSelect || len(listing.Roots) != 0 {
		t.Fatal("default browsing depends on external selection", response.Code, response.Body.String(), err)
	}
	if r := get(root); r.Code == 200 {
		t.Fatal("managed browsing enabled external selection", r.Body.String())
	}
	// A replaced managed root cannot turn the exception into another member's scope.
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	m.workspaceRoots = &[]string{root}
	response = get("")
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != 200 || listing.Path != root || listing.CurrentDirectoryError != "directory_not_found" {
		t.Fatal("missing default did not fall back to allowed root", response.Code, response.Body.String(), err)
	}
	if err := os.Symlink(otherDir, dir); err != nil {
		t.Fatal(err)
	}
	if r := get(dir); r.Code == 200 {
		t.Fatal("followed a replaced managed root", r.Body.String())
	}
}

func TestWorkspaceDirectoryBrowserPathsAndIdentity(t *testing.T) {
	m, root := directoryTestManager(t)
	room := managerCreate(t, m, "browse-directories")
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	q := url.Values{"room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "agent_id": {member.AgentID}, "membership_id": {member.MembershipID}}
	get := func(q url.Values) *httptest.ResponseRecorder {
		return managerHTTP(m, "/v1/workspace/directories?"+q.Encode(), nil)
	}
	child := filepath.Join(root, "中文 <project>")
	for _, dir := range []string{child, filepath.Join(root, "empty"), filepath.Join(root, "protected"), filepath.Join(child, "nested")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	m.workspaceProtected = append(m.workspaceProtected, filepath.Join(root, "protected"))
	if err := os.WriteFile(filepath.Join(root, "secret-file"), []byte("do not read"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	for name, target := range map[string]string{"inside-link": child, "outside-link": outside, "broken": filepath.Join(root, "absent"), "loop": "loop"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	w := get(q)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	var listing workspaceDirectoryListing
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	defaultDir, err := filepath.EvalSymlinks(filepath.Join(m.roomDir(room.RoomID), member.Workspace))
	if err != nil {
		t.Fatal(err)
	}
	if listing.Path != defaultDir || listing.Parent != nil || !listing.CanSelect || listing.DirectorySource != "managed" {
		t.Fatal(listing)
	}
	q.Set("path", root)
	w = get(q)
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Path != root || listing.Parent != nil || !listing.CanSelect || len(listing.Entries) != 3 {
		t.Fatal(listing)
	}
	for _, e := range listing.Entries {
		if strings.Contains(e.Name, "secret") || e.Name == "nested" || e.Name == "protected" || !pathWithin(root, e.Path) {
			t.Fatal("unexpected entry", e)
		}
	}
	q.Set("path", filepath.Join(root, "inside-link"))
	w = get(q)
	json.Unmarshal(w.Body.Bytes(), &listing)
	if w.Code != 200 || listing.Path != child || listing.Parent == nil || *listing.Parent != root {
		t.Fatal(w.Code, w.Body.String())
	}
	q.Set("path", filepath.Join(root, "empty"))
	w = get(q)
	json.Unmarshal(w.Body.Bytes(), &listing)
	if w.Code != 200 || len(listing.Entries) != 0 || !listing.CanSelect {
		t.Fatal(w.Code, w.Body.String())
	}
	for path, code := range map[string]string{outside: "directory_not_allowed", filepath.Join(root, "protected"): "directory_not_allowed", filepath.Join(root, "absent"): "directory_not_found", filepath.Join(root, "secret-file"): "directory_not_directory", filepath.Join(root, "loop"): "directory_unavailable"} {
		q.Set("path", path)
		w = get(q)
		if w.Code == 200 || !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	q.Del("path")
	for key := range map[string]bool{"room_instance": true, "service_epoch": true, "membership_id": true, "agent_id": true, "caller_id": true} {
		copy, _ := url.ParseQuery(q.Encode())
		copy.Set(key, "")
		if w := get(copy); w.Code == 200 {
			t.Fatal("identity accepted", key)
		}
	}
	// Directory browsing does not depend on an active per-Room runtime.
	m.index.WorkspaceOperations = append(m.index.WorkspaceOperations, workspaceOperation{RoomIdentity: room.RoomIdentity, OperationID: strings.Repeat("a", 32), Status: "running", Phase: "waiting"})
	if w := get(q); w.Code != 200 {
		t.Fatal("operation blocked browser", w.Code, w.Body.String())
	}
}

func TestWorkspaceDirectoryBrowserRootsAndBounds(t *testing.T) {
	m, root := directoryTestManager(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	m.workspaceRoots = &[]string{}
	listing, err := m.browseWorkspaceDirectory(r, "", root)
	if err != nil || listing.UnavailableReason != "directory_selection_disabled" || listing.CanSelect || len(listing.Roots) != 0 {
		t.Fatal(listing, err)
	}
	m.workspaceRoots = &[]string{filepath.Join(root, "missing"), root, root}
	for i := 0; i < 1002; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("d-%04d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	listing, err = m.browseWorkspaceDirectory(r, "", "")
	if err != nil || !listing.Truncated || len(listing.Entries) != 1000 || len(listing.Roots) != 2 || listing.Roots[0].UnavailableReason != "directory_not_found" {
		t.Fatal(len(listing.Entries), listing.Roots, err)
	}
	// Unavailable-root diagnostics have the same JSON byte bound as listings.
	hugeRoots := []string{}
	for i := 0; i < 16; i++ {
		hugeRoots = append(hugeRoots, "/"+strings.Repeat("<", 4080)+fmt.Sprint(i))
	}
	m.workspaceRoots = &hugeRoots
	unavailable, err := m.browseWorkspaceDirectory(r, "", "")
	encoded, _ := json.Marshal(unavailable)
	if err != nil || !unavailable.Truncated || len(encoded)+1 > 256<<10 || unavailable.CanSelect {
		t.Fatal(len(encoded), err)
	}
	m.workspaceRoots = &[]string{root}
	// Enumeration cap counts files too, without returning file names.
	for i := 0; i < 4100; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	listing, err = m.browseWorkspaceDirectory(r, root, "")
	if err != nil || !listing.Truncated {
		t.Fatal(listing, err)
	}
	body, _ := json.Marshal(listing)
	if len(body)+1 > 256<<10 {
		t.Fatal("oversize response", len(body))
	}
	// Long escaped paths hit the byte cap before the entry count cap.
	deep := root
	for i := 0; i < 3; i++ {
		deep = filepath.Join(deep, strings.Repeat("x", 150))
		if err := os.Mkdir(deep, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 400; i++ {
		if err := os.Mkdir(filepath.Join(deep, fmt.Sprintf("%03d%s", i, strings.Repeat("<", 80))), 0700); err != nil {
			t.Fatal(err)
		}
	}
	listing, err = m.browseWorkspaceDirectory(r, deep, "")
	body, _ = json.Marshal(listing)
	if err != nil || !listing.Truncated || len(body)+1 > 256<<10 || len(listing.Entries) >= 400 {
		t.Fatal(len(body), len(listing.Entries), err)
	}
}

func TestWorkspaceDirectoryBrowserHumanAccess(t *testing.T) {
	a, token, h := accessFixture(t)
	binding := executionBinding{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, ServiceEpoch: "epoch"}, Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}}
	execution, revoke, err := a.issueExecution(binding)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	path := "/v1/workspace/directories?caller_id=" + url.QueryEscape(a.space.OwnerUserID)
	for _, credential := range []string{"", execution} {
		if w := accessRequest(h, "GET", path, credential, "", "", nil); w.Code == 200 {
			t.Fatal("non-human allowed", w.Code)
		}
	}
	if w := accessRequest(h, "GET", path, token, "http://evil.invalid", "", nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := accessRequest(h, "GET", path, token, "", "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := accessLogin(t, h, token)
	if w := accessRequest(h, "GET", path, "", "http://127.0.0.1:8088", "", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	a.disabled = true
	if w := accessRequest(h, "GET", path, "", "", "", nil); w.Code != 200 {
		t.Fatal("local rule changed", w.Code)
	}
}

func TestWorkspaceDirectoryBrowserDefaultHome(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Unix home policy")
	}
	m, root := directoryTestManager(t)
	m.workspaceRoots = nil
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	t.Setenv("HOME", root)
	result, err := m.browseWorkspaceDirectory(r, "", "")
	if err != nil || result.Path != root || !result.CanSelect {
		t.Fatal(result, err)
	}
	for _, home := range []string{"/", filepath.Join(root, "not-mounted")} {
		t.Setenv("HOME", home)
		result, err = m.browseWorkspaceDirectory(r, "", "")
		if err != nil || result.CanSelect || result.UnavailableReason != "directory_home_unavailable" {
			t.Fatal(result, err)
		}
	}
}
