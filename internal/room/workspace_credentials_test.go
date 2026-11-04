package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func saveTestWorkspaceCredential(t *testing.T, m *roomManager, owner RoomIdentity, value string) string {
	t.Helper()
	id, err := m.saveWorkspaceCredential(owner, value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setTestEffectiveWorkspace(t *testing.T, m *roomManager, room roomSummary, w workspaceConfig, version uint64) {
	t.Helper()
	c, _ := readManagedRoom(t, m, room)
	c.Workspace, c.WorkspaceVersion = &w, version
	for i := range c.Members {
		c.Members[i].Workspace = workspaceMemberPath(w, c.Members[i].MembershipID)
	}
	if err := m.store.save(filepath.Join(m.roomDir(room.RoomID), "room-config.json"), c, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
}

func credentialTestWorkspace(id string) workspaceConfig {
	w := testGitLabWorkspace()
	w.Binding.TokenEnv, w.Binding.CredentialID = "", id
	return w
}

func TestWorkspaceCredentialsOwnershipPersistenceAndRedaction(t *testing.T) {
	root := t.TempDir()
	profiles := managerTestProfiles(t)
	m := testManager(t, root, profiles)
	a, b := managerCreate(t, m, "credential-a"), managerCreate(t, m, "credential-b")
	const pat = "test-private-token-never-in-room-data"
	id := saveTestWorkspaceCredential(t, m, a.RoomIdentity, pat)
	secret, err := m.loadWorkspaceCredential(a.RoomIdentity, id)
	if err != nil || secret.value != pat {
		t.Fatal("could not resolve owner credential", err)
	}
	encoded, err := json.Marshal(secret)
	if err != nil || bytes.Contains(encoded, []byte(pat)) || strings.Contains(fmt.Sprintf("%v %+v %#v %s %q", secret, secret, secret, secret, secret), pat) {
		t.Fatal("generic serialization or formatting disclosed a credential")
	}
	for _, owner := range []RoomIdentity{b.RoomIdentity, {RoomID: a.RoomID, RoomInstance: "wrong-generation"}} {
		if _, err := m.loadWorkspaceCredential(owner, id); !errors.Is(err, ErrIdentity) {
			t.Fatal("cross-room or stale-generation access", err)
		}
	}
	if _, err := m.loadWorkspaceCredential(a.RoomIdentity, "../credential"); !errors.Is(err, ErrIdentity) {
		t.Fatal("credential ID path traversal", err)
	}
	op := testWorkspaceOperation(t, m, a, credentialTestWorkspace(id))
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{m.indexPath(), filepath.Join(m.roomDir(a.RoomID), "room-config.json")} {
		data, err := os.ReadFile(name)
		if err != nil || bytes.Contains(data, []byte(pat)) {
			t.Fatal("PAT in public configuration or operation record", err)
		}
	}
	for name, mode := range map[string]os.FileMode{filepath.Join(root, "credentials"): 0700, filepath.Join(root, "credentials", id+".json"): 0600} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("wrong credential permissions", err)
		}
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m, err = newRoomManager(root, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	secret, err = m.loadWorkspaceCredential(a.RoomIdentity, id)
	if err != nil || secret.value != pat {
		t.Fatal("credential did not survive restart", err)
	}
	// A different room cannot even reserve a pending reference to this PAT.
	op = testWorkspaceOperation(t, m, b, credentialTestWorkspace(id))
	if err := m.saveWorkspaceOperation(op); !errors.Is(err, ErrIdentity) {
		t.Fatal("cross-room pending credential reference", err)
	}
}

func TestWorkspaceCredentialReplacementAndReferenceCleanup(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	a, b := managerCreate(t, m, "replace-a"), managerCreate(t, m, "replace-b")
	old := saveTestWorkspaceCredential(t, m, a.RoomIdentity, "old-token")
	newID := saveTestWorkspaceCredential(t, m, a.RoomIdentity, "new-token")
	orphan := saveTestWorkspaceCredential(t, m, a.RoomIdentity, "orphan-token")
	other := saveTestWorkspaceCredential(t, m, b.RoomIdentity, "other-room-token")
	if old == newID {
		t.Fatal("replacement reused a credential ID")
	}
	setTestEffectiveWorkspace(t, m, a, credentialTestWorkspace(old), 1)
	op := testWorkspaceOperation(t, m, a, credentialTestWorkspace(newID))
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	// A damaged reference source prevents cleanup, including deletion of what
	// otherwise appears to be an orphan. Preserve the entire pre-cleanup set.
	bConfig := filepath.Join(m.roomDir(b.RoomID), "room-config.json")
	before, err := os.ReadFile(bConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bConfig, []byte(`{"unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.cleanupWorkspaceCredentials(a.RoomIdentity); err == nil {
		t.Fatal("cleanup ignored unknown config references")
	}
	if _, err := m.loadWorkspaceCredential(a.RoomIdentity, orphan); err != nil {
		t.Fatal("cleanup deleted before checking all references", err)
	}
	if err := os.WriteFile(bConfig, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.cleanupWorkspaceCredentials(a.RoomIdentity); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old, newID, other} {
		if _, err := os.Stat(filepath.Join(m.store.dir, "credentials", id+".json")); err != nil {
			t.Fatal("deleted referenced or other-room credential", err)
		}
	}
	if _, err := os.Stat(filepath.Join(m.store.dir, "credentials", orphan+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphan credential retained", err)
	}
	// Failed operations still pin their credential for explicit recovery.
	op.Status, op.ErrorCode = "failed", "preparation_failed"
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	if err := m.cleanupWorkspaceCredentials(a.RoomIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := m.loadWorkspaceCredential(a.RoomIdentity, newID); err != nil {
		t.Fatal("failed operation's credential was removed", err)
	}
	setTestEffectiveWorkspace(t, m, a, op.Target, op.TargetVersion)
	op.Status, op.ErrorCode, op.Phase, op.ConfigCommitted = "completed", "", "completed", true
	for _, member := range op.Members {
		op.SessionsUpdated = append(op.SessionsUpdated, member.MembershipID)
	}
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	if err := m.cleanupWorkspaceCredentials(a.RoomIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := m.loadWorkspaceCredential(a.RoomIdentity, old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old unreferenced PAT retained", err)
	}
	if secret, err := m.loadWorkspaceCredential(a.RoomIdentity, newID); err != nil || secret.value != "new-token" {
		t.Fatal("replacement no longer resolves", err)
	}
}

func TestWorkspaceCredentialDeletionIsScopedToSettledRoom(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	a, b := managerCreate(t, m, "delete-credentials"), managerCreate(t, m, "keep-credentials")
	aID := saveTestWorkspaceCredential(t, m, a.RoomIdentity, "delete-me")
	bID := saveTestWorkspaceCredential(t, m, b.RoomIdentity, "keep-me")
	op := testWorkspaceOperation(t, m, a, credentialTestWorkspace(aID))
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	beginDeletion(t, m, a)
	if remaining := awaitDeletion(t, m, a); remaining != nil {
		t.Fatal("deletion did not finish", remaining)
	}
	if _, err := os.Stat(filepath.Join(m.store.dir, "credentials", aID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted room retained credential", err)
	}
	if len(m.index.WorkspaceOperations) != 0 {
		t.Fatal("deleted room retained workspace operation")
	}
	if secret, err := m.loadWorkspaceCredential(b.RoomIdentity, bID); err != nil || secret.value != "keep-me" {
		t.Fatal("deletion affected another room", err)
	}
}

func TestWorkspaceCredentialsRejectUnsafeStorageAndMalformedValues(t *testing.T) {
	for _, kind := range []string{"directory-symlink", "file-symlink", "directory-permissions", "file-permissions", "bad-json", "wrong-id", "unknown-field"} {
		t.Run(kind, func(t *testing.T) {
			m := testManager(t, t.TempDir(), managerTestProfiles(t))
			room := managerCreate(t, m, "unsafe")
			id := saveTestWorkspaceCredential(t, m, room.RoomIdentity, "do-not-reflect-this-PAT")
			dir := filepath.Join(m.store.dir, "credentials")
			name := filepath.Join(dir, id+".json")
			switch kind {
			case "directory-symlink":
				os.Rename(dir, dir+"-original")
				os.Symlink(dir+"-original", dir)
			case "file-symlink":
				os.Rename(name, name+"-original")
				os.Symlink(name+"-original", name)
			case "directory-permissions":
				os.Chmod(dir, 0755)
			case "file-permissions":
				os.Chmod(name, 0644)
			case "bad-json":
				os.WriteFile(name, []byte(`{"pat":"do-not-reflect-this-PAT",`), 0600)
			case "wrong-id":
				data, _ := os.ReadFile(name)
				os.WriteFile(name, bytes.ReplaceAll(data, []byte(id), []byte(strings.Repeat("f", 32))), 0600)
			case "unknown-field":
				os.WriteFile(name, []byte(`{"do-not-reflect-this-PAT":true}`), 0600)
			}
			if _, err := m.loadWorkspaceCredential(room.RoomIdentity, id); err == nil || strings.Contains(err.Error(), "do-not-reflect-this-PAT") {
				t.Fatal("unsafe storage accepted or error disclosed secret")
			}
			if err := m.cleanupWorkspaceCredentials(room.RoomIdentity); err == nil {
				t.Fatal("cleanup did not preserve malformed evidence")
			}
		})
	}
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "invalid-values")
	for _, value := range []string{"", " leading", "trailing ", "line\nbreak", "nul\x00byte", strings.Repeat("x", 8193)} {
		if _, err := m.saveWorkspaceCredential(room.RoomIdentity, value); !errors.Is(err, ErrRequest) {
			t.Fatal("invalid PAT accepted", err)
		}
	}
	// Worst-case JSON escaping remains within the bounded credential file.
	id := saveTestWorkspaceCredential(t, m, room.RoomIdentity, strings.Repeat("\\", 8192))
	if _, err := m.loadWorkspaceCredential(room.RoomIdentity, id); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCredentialUncertainWritesAndCleanupFreezeManager(t *testing.T) {
	for _, kind := range []string{"before-rename", "after-rename", "directory-sync", "cleanup-sync"} {
		t.Run(kind, func(t *testing.T) {
			m := testManager(t, t.TempDir(), managerTestProfiles(t))
			room := managerCreate(t, m, "credential-uncertain")
			if kind == "cleanup-sync" {
				saveTestWorkspaceCredential(t, m, room.RoomIdentity, "orphan")
			}
			rename := m.store.ops.rename
			if strings.Contains(kind, "rename") {
				m.store.ops.rename = func(from, to string) error {
					if kind == "after-rename" {
						if err := rename(from, to); err != nil {
							return err
						}
					}
					return errors.New("lost credential replacement result")
				}
			} else {
				m.store.ops.syncDir = func(string) error { return errors.New("lost directory sync result") }
			}
			var err error
			if kind == "cleanup-sync" {
				err = m.cleanupWorkspaceCredentials(room.RoomIdentity)
			} else {
				_, err = m.saveWorkspaceCredential(room.RoomIdentity, "new-token")
			}
			if !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("ambiguous durability was not latched", err)
			}
			if _, err := m.create(roomSelection{CreateID: "must-not-write", TeamID: "takeout-dinner"}); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("uncertain credential operation allowed catalog writes", err)
			}
		})
	}
}
