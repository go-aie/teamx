package room

import (
	"context"
	"errors"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Only the latest operation is retained. Version is the existing directory
// version; request ID is also the durable browser cache invalidation marker.
type spaceResetOperation struct {
	FormatVersion   int    `json:"format_version"`
	RequestID       string `json:"request_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	Phase           string `json:"phase"`
	Code            string `json:"code,omitempty"`
	Message         string `json:"message,omitempty"`
}

func (op *spaceResetOperation) valid() bool {
	if op == nil {
		return true
	}
	if op.FormatVersion != 1 || !validSpaceID(op.RequestID) || op.ExpectedVersion == 0 {
		return false
	}
	switch op.Phase {
	case "stopping", "cleaning", "initializing", "completed":
		return true
	}
	return false
}

func (s *spaceServer) serveSpaceReset(w http.ResponseWriter, r *http.Request, input spaceChangeInput) {
	// Gate before any mutation, including retries, for unaccepted compositions.
	if !s.spaceResetAvailable() {
		accessError(w, 403, "space_reset_unavailable")
		return
	}
	if input.Name != "" || !validSpaceID(input.RequestID) || input.ExpectedVersion == 0 {
		writeAPIError(w, ErrRequest)
		return
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/spaces/"), "/")
	scope, _ := r.Context().Value(spaceContextKey{}).(authorizedSpace)
	record, err := s.requestSpaceReset(id, input, scope.Subject)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if record.Reset.Phase != "completed" {
		w.WriteHeader(http.StatusAccepted)
	}
	_ = writeJSON(w, record)
}

func (s *spaceServer) spaceResetAvailable() bool {
	return s.access.users == nil || s.assembly.factories.hostedResetAccepted
}

func (s *spaceServer) requestSpaceReset(id string, input spaceChangeInput, subject userSubject) (SpaceRecord, error) {
	if !s.spaceResetAvailable() || s.access.authorizeSubject(subject) != nil {
		return SpaceRecord{}, ErrIdentity
	}
	if !validSpaceID(input.RequestID) || input.ExpectedVersion == 0 || input.Name != "" {
		return SpaceRecord{}, ErrRequest
	}
	// Serialize request acceptance with administrative disable. No identity or
	// directory mutex is held during preflight, and no execution is waited here.
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	if s.access.authorizeSubject(subject) != nil {
		return SpaceRecord{}, ErrIdentity
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return SpaceRecord{}, ErrClosing
	}
	index := -1
	var record SpaceRecord
	for i, current := range s.directory.Spaces {
		if current.ID == id && current.OwnerUserID == subject.UserID {
			index, record = i, current
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		return SpaceRecord{}, ErrIdentity
	}
	if !record.Reset.valid() {
		s.mu.Unlock()
		return SpaceRecord{}, ErrInvalid
	}
	same := record.Reset != nil && record.Reset.RequestID == input.RequestID
	if same && record.Reset.ExpectedVersion != input.ExpectedVersion {
		s.mu.Unlock()
		return SpaceRecord{}, ErrConflict
	}
	if same && record.Reset.Phase == "completed" {
		if s.resetBusy[id] {
			s.mu.Unlock()
			return record, nil
		}
		// A failed new assembly is retried independently of business cleanup.
		s.retryResetAssemblyLocked(record)
		s.mu.Unlock()
		return record, nil
	}
	if !same && (record.resetting() || s.resetBusy[id]) {
		s.mu.Unlock()
		return SpaceRecord{}, ErrBusy
	}
	if !same && record.Version != input.ExpectedVersion {
		s.mu.Unlock()
		return SpaceRecord{}, ErrConflict
	}
	entry := s.entries[id]
	if !same && (record.Status == "archiving" || entry != nil && entry.lifecycleBusy) {
		s.mu.Unlock()
		return SpaceRecord{}, ErrBusy
	}
	if s.resetBusy[id] {
		s.mu.Unlock()
		return record, nil
	}
	if s.resetBusy == nil {
		s.resetBusy = map[string]bool{}
	}
	s.resetBusy[id] = true
	s.lifecycle.Add(1) // includes preflight; shutdown cannot release ownership under it
	s.mu.Unlock()
	handedOff := false
	defer func() {
		if !handedOff {
			s.mu.Lock()
			delete(s.resetBusy, id)
			s.mu.Unlock()
			s.lifecycle.Done()
		}
	}()
	var prepared *spaceResetStorage
	if !same {
		op := &spaceResetOperation{FormatVersion: 1, RequestID: input.RequestID, ExpectedVersion: input.ExpectedVersion, Phase: "stopping"}
		record.Reset = op
		storage, err := s.resetStorage(record)
		if err != nil {
			return SpaceRecord{}, err
		}
		doc, err := s.resetDocument(record, entry)
		if err != nil {
			return SpaceRecord{}, resetConfigDiagnostic(err)
		}
		doc.mu.Lock()
		err = doc.unchanged()
		if err == nil {
			_, err = spaceResetConfig(doc.source)
		}
		doc.mu.Unlock()
		if err != nil {
			return SpaceRecord{}, resetConfigDiagnostic(err)
		}
		if err := storage.saveCandidate(doc); err != nil {
			return SpaceRecord{}, err
		}
		prepared = storage
		s.mu.Lock()
		if s.closing || s.directory.Spaces[index].Version != input.ExpectedVersion || s.access.authorizeSubject(subject) != nil {
			s.mu.Unlock()
			return SpaceRecord{}, ErrConflict
		}
		record.Version++
		if record.Status == "active" {
			record.Status = "archiving"
		}
		err = s.saveSpaceLocked(index, record)
		if err != nil {
			if errors.Is(err, ErrCommitUnknown) {
				if record.ID == s.access.space.SpaceID {
					s.access.configFenced.Store(true)
				}
				if entry != nil {
					entry.revoke()
					entry.lifecycleBusy = true
					entry.lifecycleError = "重置登记结果尚未确定，请重启服务后核对并继续原操作。"
				}
				handedOff = true
				s.lifecycle.Done() /* busy fence remains until restart */
			}
			s.mu.Unlock()
			return SpaceRecord{}, err
		}
		if record.ID == s.access.space.SpaceID {
			s.access.configFenced.Store(true)
		}
		if entry != nil {
			entry.revoke()
		}
		s.mu.Unlock()
	}
	handedOff = true
	go s.runSpaceReset(record, entry, prepared)
	return record, nil
}

func (s *spaceServer) retryResetAssemblyLocked(record SpaceRecord) {
	if record.Status != "active" || s.closing {
		return
	}
	entry := s.entries[record.ID]
	if entry != nil {
		select {
		case <-entry.done:
		default:
			return
		}
		entry.mu.RLock()
		r := entry.runtime
		entry.mu.RUnlock()
		if r != nil && r.State() != "stopped" {
			return
		}
	}
	delete(s.entries, record.ID)
	s.startLocked(record, nil, spaceAddress(strings.TrimPrefix(s.access.internal, "http://")))
}
func (s *spaceServer) resetDocument(record SpaceRecord, entry *spaceServerEntry) (*configDocument, error) {
	if entry != nil {
		entry.mu.RLock()
		runtime := entry.runtime
		entry.mu.RUnlock()
		if runtime != nil {
			return runtime.config.settings.configDocument, nil
		}
	}
	path := filepath.Join(record.Roots.Home, "config.toml")
	if record.ID == s.access.space.SpaceID {
		path = s.assembly.config.settings.path
	}
	data, file, err := readSettingsFile(path)
	if err != nil {
		return nil, err
	}
	return &configDocument{path: path, source: data, file: file, private: true, ops: defaultSnapshotOps()}, nil
}
func (s *spaceServer) resetPhase(record SpaceRecord, phase string) (SpaceRecord, error) {
	if phase == "completed" {
		// Never wait for a worker under adminMu: disable settlement can itself
		// observe this operation. Only its final publication needs serialization.
		s.adminMu.Lock()
		defer s.adminMu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.ctx.Err() != nil {
		return record, ErrClosing
	}
	for i, current := range s.directory.Spaces {
		if current.ID != record.ID {
			continue
		}
		if current.ResetID() != record.ResetID() {
			return record, ErrConflict
		}
		op := *current.Reset
		op.Phase, op.Code, op.Message = phase, "", ""
		current.Reset = &op
		current.Status = "suspended"
		current.Version++
		if phase == "completed" {
			current.Status = "active"
			if s.access.users != nil {
				user, ok := s.access.users.user(current.OwnerUserID)
				if !ok || user.Status != "active" {
					current.Status = "suspended"
					op.Code, op.Message = "owner_unavailable", "重置已完成，所属用户不可用。启用用户后可恢复空间。"
				}
			}
			next := append([]SpaceRecord{}, s.directory.Spaces...)
			next[i] = current
			if err := s.assembly.config.executionResources.checkSpaces(next); current.Status == "active" && err != nil {
				current.Status = "suspended"
				op.Code = "resource_unavailable"
				op.Message = "重置已完成，资源暂不可用。请稍后恢复空间。"
			}
		}
		if err := s.saveSpaceLocked(i, current); err != nil {
			return record, err
		}
		return current, nil
	}
	return record, ErrIdentity
}
func (s *spaceServer) runSpaceReset(record SpaceRecord, entry *spaceServerEntry, prepared ...*spaceResetStorage) {
	var runErr error
	defer func() {
		s.mu.Lock()
		// Uncertain writes require restart reconciliation; do not replace a latched
		// writer or guess which directory version committed.
		if !errors.Is(runErr, ErrCommitUnknown) {
			for _, current := range s.directory.Spaces {
				if current.ID == record.ID && current.ResetID() == record.ResetID() {
					delete(s.resetBusy, record.ID)
					break
				}
			}
		}
		if runErr != nil && !s.closing && !errors.Is(runErr, ErrCommitUnknown) {
			for i, current := range s.directory.Spaces {
				if current.ID == record.ID && current.ResetID() == record.ResetID() && current.resetting() {
					op := *current.Reset
					op.Code, op.Message = spaceResetFailure(current.Reset.Phase, runErr)
					current.Reset = &op
					current.Version++
					if err := s.saveSpaceLocked(i, current); errors.Is(err, ErrCommitUnknown) {
						s.resetBusy[record.ID] = true
					}
					break
				}
			}
		}
		s.mu.Unlock()
		s.lifecycle.Done()
	}()
	if !record.Reset.valid() {
		runErr = ErrInvalid
		return
	}
	if record.Reset.Phase == "stopping" && entry != nil {
		entry.revoke()
		ctx, cancel := context.WithTimeout(s.ctx, serviceShutdownTimeout)
		runErr = s.settleSpace(ctx, entry)
		cancel()
		if !backendCleanupSettled(runErr) {
			return
		}
		runErr = nil
	}
	storage, err := s.resetStorage(record)
	if len(prepared) > 0 && prepared[0] != nil {
		storage = prepared[0]
		err = storage.validate()
	}
	if err != nil {
		runErr = err
		return
	}
	if _, err := storage.loadCandidate(); err != nil {
		runErr = err
		return
	}
	// Preserve the Home and Data lock inodes. Default Home may already be held by
	// the deployment; additional Spaces hand their Home lock back on close.
	var locks []*os.File
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			runErr = errors.Join(runErr, unlockDirectory(locks[i]))
		}
	}()
	if record.ID != s.access.space.SpaceID || s.homeLock == nil {
		lock, err := lockBundleHome(record.Roots.Home)
		if err != nil {
			runErr = err
			return
		}
		locks = append(locks, lock)
	}
	for _, root := range []string{record.Roots.Data, record.Roots.Teams} {
		if err := makeDurableDir(root); err != nil {
			runErr = err
			return
		}
		lock, err := lockDirectory(root)
		if err != nil {
			runErr = err
			return
		}
		locks = append(locks, lock)
	}
	if err := storage.validate(); err != nil {
		runErr = err
		return
	}
	if record.Reset.Phase == "stopping" {
		if record.ID == s.access.space.SpaceID {
			// Join config writes admitted before the durable fence. New writes see
			// configFenced under this same access lock and cannot race the candidate.
			s.access.mu.Lock()
			s.access.mu.Unlock()
		}
		// Also used on process restart: no Go entry is not proof of execution exit.
		if err := checkSpaceResetSettled(record.Roots.Data); err != nil {
			runErr = err
			return
		}
		doc, err := s.resetDocument(record, entry)
		if err != nil {
			runErr = err
			return
		}
		if err := storage.saveCandidate(doc); err != nil {
			runErr = err
			return
		}
		record, runErr = s.resetPhase(record, "cleaning")
		if runErr != nil {
			return
		}
	}
	candidate, err := storage.loadCandidate()
	if err != nil {
		runErr = err
		return
	}
	if record.Reset.Phase == "cleaning" {
		if err := storage.clean(); err != nil {
			runErr = err
			return
		}
		record, runErr = s.resetPhase(record, "initializing")
		if runErr != nil {
			return
		}
	}
	if record.Reset.Phase == "initializing" {
		if err := storage.initialize(candidate); err != nil {
			runErr = err
			return
		}
		if s.claims != nil {
			if err := s.claims.releaseResetSpace(record.ID); err != nil {
				runErr = err
				return
			}
		}
		record, runErr = s.resetPhase(record, "completed")
		if runErr != nil {
			return
		}
	}
	// Candidate contains hashes and root identities only, never configuration or
	// secrets. A crash here is cleaned at startup without rerunning initialization.
	_ = os.Remove(storage.candidatePath)
	_ = defaultSnapshotOps().syncDir(filepath.Dir(storage.candidatePath))
	for i := len(locks) - 1; i >= 0; i-- {
		if err := unlockDirectory(locks[i]); err != nil {
			runErr = err
			return
		}
	}
	locks = nil
	runErr = s.finishSpaceReset(record)
}

func (s *spaceServer) finishSpaceReset(record SpaceRecord) error {
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, current := range s.directory.Spaces {
		if current.ID != record.ID || current.ResetID() != record.ResetID() {
			continue
		}
		// Disable may have arrived after completed was published but before the
		// cleanup locks were handed back. It must still leave a settled Space.
		if s.access.users != nil && current.Status == "active" {
			user, ok := s.access.users.user(current.OwnerUserID)
			if !ok || user.Status != "active" {
				op := *current.Reset
				op.Code, op.Message = "owner_unavailable", "重置已完成，所属用户不可用。启用用户后可恢复空间。"
				current.Reset, current.Status = &op, "suspended"
				current.Version++
				if err := s.saveSpaceLocked(i, current); err != nil {
					return err
				}
			}
		}
		delete(s.entries, record.ID)
		delete(s.resetBusy, record.ID)
		if !s.closing && current.Status == "active" {
			s.startLocked(current, nil, spaceAddress(strings.TrimPrefix(s.access.internal, "http://")))
		}
		return nil
	}
	return ErrConflict
}

func checkSpaceResetSettled(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && isSessionGenerationEntry(d.Name()) {
			if err := checkSessionGenerationSettled(path); err != nil {
				return err
			}
			return fs.SkipDir
		}
		return nil
	})
}

// Read the control record before model migration or index restoration. Unknown
// operation formats stay fenced, and are surfaced through the control page.
func startupResetPending(startup startupConfig) (bool, error) {
	var directory spaceDirectory
	err := readIdentitySnapshot(filepath.Join(filepath.Dir(startup.path), "spaces.json"), &directory)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, record := range directory.Spaces {
		if record.ID == directory.DefaultSpaceID {
			return record.resetting(), nil
		}
	}
	return false, nil
}

func homeHasPendingReset(home string, owners ...uint32) (bool, error) {
	owner := uint32(os.Geteuid())
	if len(owners) > 0 {
		owner = owners[0]
	}
	var directory spaceDirectory
	err := readIdentitySnapshotForUID(filepath.Join(home, "spaces.json"), &directory, owner)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, record := range directory.Spaces {
		if record.resetting() {
			return true, nil
		}
	}
	return false, nil
}
func rejectPendingSpaceReset(home string, owners ...uint32) error {
	// Deployment commands may run as root while the directory belongs to the
	// registered service account. Never infer a trusted owner from the file.
	pending, err := homeHasPendingReset(home, owners...)
	if err != nil {
		return err
	}
	if pending {
		return errors.New("空间重置尚未完成，请先继续原操作，再执行初始化、备份或升级")
	}
	return nil
}

func spaceResetFailure(phase string, err error) (string, string) {
	switch {
	case errors.Is(err, ErrLocked):
		return "directory_busy", "空间目录仍被占用，未继续清理。请等待原任务结清后重试。"
	case errors.Is(err, ErrConflict):
		return "config_conflict", "配置或初始化内容已变化，空间保持封闭。请核对配置后重启并继续原重置。"
	case errors.Is(err, dsh.ErrRecoveryUnsupported):
		return "execution_unsettled", "无法证明旧 Agent 执行已停止，保留原数据。请先核对执行结清情况，再重试原操作。"
	}
	switch phase {
	case "stopping":
		return "stop_failed", "旧任务尚未结清，保留原数据。请等待任务退出后重试。"
	case "cleaning":
		return "clean_failed", "清理文件失败，空间保持封闭。请检查目录权限和占用后重试。"
	case "initializing":
		return "initialize_failed", "初始化配置或模板失败，空间保持封闭。请核对部署模板及配置后重试。"
	default:
		return "unsupported_reset", "无法识别此重置操作，空间保持封闭。请使用兼容版本核对。"
	}
}
