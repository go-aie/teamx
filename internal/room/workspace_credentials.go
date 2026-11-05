package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

const maxWorkspaceCredentialBytes int64 = 32 << 10

// A resolved credential is deliberately opaque to generic JSON and formatting.
// Only the explicit disk writer and future credential injection read value.
type workspaceSecret struct{ value string }

func (workspaceSecret) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }
func (workspaceSecret) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, "[REDACTED]") }

type workspaceCredentialMetadata struct {
	Version int `json:"version"`
	RoomIdentity
	CredentialID string    `json:"credential_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// Only this private on-disk DTO carries the plaintext PAT. Never return it from
// a resolver, place it in an operation, or include it in an API response.
type workspaceCredentialFile struct {
	workspaceCredentialMetadata
	PAT string `json:"pat"`
}

func (workspaceCredentialFile) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED credential file]")
}

func validWorkspacePAT(pat string) bool {
	if len(pat) == 0 || len(pat) > 8192 {
		return false
	}
	for _, c := range pat {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}

// Save always allocates a new ID: replacement never overwrites a credential
// still referenced by an effective config or unfinished management operation.
func (m *roomManager) saveWorkspaceCredential(owner RoomIdentity, pat string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveWorkspaceCredentialLocked(owner, pat)
}

func (m *roomManager) saveWorkspaceCredentialLocked(owner RoomIdentity, pat string) (string, error) {
	if m.closing {
		return "", ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return "", err
	}
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" {
		return "", ErrIdentity
	}
	if !validWorkspacePAT(pat) {
		return "", fmt.Errorf("%w: invalid PAT", ErrRequest)
	}
	if err := m.workspaceCredentialDirectory(true); err != nil {
		return "", err
	}
	id, err := newOpaqueID()
	if err != nil {
		return "", err
	}
	file := workspaceCredentialFile{workspaceCredentialMetadata: workspaceCredentialMetadata{
		Version: formatVersion, RoomIdentity: owner, CredentialID: id, CreatedAt: time.Now().UTC()}, PAT: pat}
	name := filepath.Join(m.store.dir, "credentials", id+".json")
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return "", ErrConflict
	}
	if err := m.store.save(name, file, maxWorkspaceCredentialBytes); err != nil {
		return "", err
	}
	return id, nil
}

func (m *roomManager) loadWorkspaceCredential(owner RoomIdentity, id string) (workspaceSecret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.check(false); err != nil {
		return workspaceSecret{}, err
	}
	if _, err := m.workspaceRoom(owner); err != nil {
		return workspaceSecret{}, err
	}
	return m.readWorkspaceCredential(owner, id)
}

// Called under the manager lock. Decode errors are replaced with static
// diagnostics so malformed credential data cannot reflect a PAT into logs.
func (m *roomManager) readWorkspaceCredential(owner RoomIdentity, id string) (workspaceSecret, error) {
	if !validRestartID(id) {
		return workspaceSecret{}, ErrIdentity
	}
	if err := m.workspaceCredentialDirectory(false); err != nil {
		return workspaceSecret{}, err
	}
	file, err := m.readWorkspaceCredentialFile(id)
	if err != nil {
		return workspaceSecret{}, err
	}
	if file.RoomIdentity != owner {
		return workspaceSecret{}, ErrIdentity
	}
	return workspaceSecret{value: file.PAT}, nil
}

func (m *roomManager) workspaceCredentialDirectory(create bool) error {
	dir := filepath.Join(m.store.dir, "credentials")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := makeDurableDir(dir); err != nil {
			return err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return fmt.Errorf("%w: credentials directory must be private and cannot be a symlink", ErrInvalid)
	}
	return nil
}

func (m *roomManager) readWorkspaceCredentialFile(id string) (workspaceCredentialFile, error) {
	var file workspaceCredentialFile
	name := filepath.Join(m.store.dir, "credentials", id+".json")
	info, err := os.Lstat(name)
	if err != nil {
		return file, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return file, fmt.Errorf("%w: credential must be a private regular file", ErrInvalid)
	}
	if err := readSnapshot(name, &file, maxWorkspaceCredentialBytes); err != nil {
		return workspaceCredentialFile{}, fmt.Errorf("%w: malformed credential file", ErrInvalid)
	}
	if file.Version != formatVersion || !safeMembershipID(file.RoomID) || !validText(file.RoomInstance) ||
		file.CredentialID != id || file.CreatedAt.IsZero() || !validWorkspacePAT(file.PAT) {
		return workspaceCredentialFile{}, fmt.Errorf("%w: invalid credential record", ErrInvalid)
	}
	return file, nil
}

// Remove only this owner's unreferenced credentials. All references and files
// are checked before the first unlink; uncertain durability freezes all writes.
func (m *roomManager) cleanupWorkspaceCredentials(owner RoomIdentity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleanupWorkspaceCredentialsLocked(owner)
}

func (m *roomManager) cleanupWorkspaceCredentialsLocked(owner RoomIdentity) error {
	if err := m.store.check(true); err != nil {
		return err
	}
	entry, err := m.workspaceRoom(owner)
	if err != nil {
		return err
	}
	removing := entry.Status == "deleting" && entry.DeletionPhase == "removing"
	if err := m.workspaceCredentialDirectory(false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	dir := filepath.Join(m.store.dir, "credentials")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	referenced := map[string]bool{}
	add := func(w *workspaceConfig) {
		if w != nil && w.Binding != nil && w.Binding.CredentialID != "" {
			referenced[w.Binding.CredentialID] = true
		}
	}
	for _, entry := range m.index.Rooms {
		if entry.Status == "deleting" && entry.DeletionPhase == "removing" {
			continue // Execution was durably settled before authorizing removal.
		}
		config, err := m.readWorkspaceRoomConfig(entry)
		if err != nil {
			return err // Unknown references must never authorize deletion.
		}
		add(config.Workspace)
		// Subscriptions retain their credential references after Git is unbound.
		for _, state := range m.notifications.StatesLocked(entry.RoomIdentity) {
			if s, ok := state.(gitlab.EventsState); ok && s.Binding.CredentialID != "" {
				referenced[s.Binding.CredentialID] = true
			}
		}
	}
	for _, op := range m.index.WorkspaceOperations {
		if !op.finished() && !(removing && op.RoomIdentity == owner) {
			add(&op.Target)
		}
	}
	var remove []string
	for _, entry := range entries {
		// Interrupted temporary writes are not records and are never loaded.
		if strings.HasPrefix(entry.Name(), ".snapshot-") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !strings.HasSuffix(entry.Name(), ".json") || !validRestartID(id) {
			return fmt.Errorf("%w: unexpected credential entry", ErrInvalid)
		}
		file, err := m.readWorkspaceCredentialFile(id)
		if err != nil {
			return err
		}
		if file.RoomIdentity == owner && !referenced[id] {
			remove = append(remove, filepath.Join(dir, entry.Name()))
		}
	}
	for _, name := range remove {
		if err := os.Remove(name); err != nil {
			m.store.uncertain = true
			return fmt.Errorf("%w: remove credential", ErrCommitUnknown)
		}
	}
	if len(remove) > 0 {
		if err := m.store.ops.syncDir(dir); err != nil {
			m.store.uncertain = true
			return fmt.Errorf("%w: sync credential removal", ErrCommitUnknown)
		}
	}
	return nil
}
