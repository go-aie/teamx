package room

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// configDocument serializes local settings/model writers and publishes memory
// only after the replacement and directory have verified durability.
type configCommit struct {
	source  []byte
	publish func()
}
type configDocument struct {
	private bool // First-use/access writers retain their existing 0600 contract.
	mu      sync.Mutex
	path    string
	source  []byte
	file    os.FileInfo
	pending *configCommit
	ops     snapshotOps
}

// Re-read both identity and bytes, including after preparing the temporary
// file. An editor's replacement or in-place edit invalidates this save.
func (s *configDocument) unchanged() error {
	data, file, err := readSettingsFile(s.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, s.source) || (file == nil) != (s.file == nil) ||
		(file != nil && (!os.SameFile(file, s.file) || !file.ModTime().Equal(s.file.ModTime()))) {
		return &requestDiagnostic{cause: ErrConflict, Message: "startup config changed outside this service; restart to load it before saving"}
	}
	return nil
}

func readSettingsFile(path string) ([]byte, os.FileInfo, error) {
	file, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	// Do not replace a symlink with a regular file or follow a newly changed link.
	if !file.Mode().IsRegular() || file.Size() > 1<<20 {
		return nil, nil, fmt.Errorf("config must be a regular file of at most 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(file, opened) {
		return nil, nil, ErrConflict
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, nil, fmt.Errorf("cannot read config (maximum 1 MiB)")
	}
	return data, opened, nil
}

// Caller holds mu. Network validation is performed before entering this method.
func (s *configDocument) commitLocked(build func([]byte) ([]byte, func(), error)) error {
	if s.path == "" {
		return errors.New("startup config path unavailable")
	}
	dir := filepath.Dir(s.path)
	if err := s.ops.makeDir(dir); err != nil {
		return err
	}
	lock, err := lockDirectoryFile(dir, "."+filepath.Base(s.path)+".lock")
	if err != nil {
		return err
	}
	defer unlockDirectory(lock)
	if s.pending != nil {
		if err := s.reconcile(); err != nil {
			return err
		}
	}
	if err := s.unchanged(); err != nil {
		return err
	}
	next, publish, err := build(s.source)
	if err != nil || next == nil {
		return err
	}
	if len(next) > 1<<20 {
		return ErrCapacity
	}
	f, err := s.ops.createTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	mode := os.FileMode(0600)
	if s.file != nil && !s.private {
		mode = s.file.Mode().Perm()
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		return err
	}
	if _, err := io.Copy(f, bytes.NewReader(next)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := s.unchanged(); err != nil {
		return err
	}
	s.pending = &configCommit{source: next, publish: publish}
	replaceErr := s.ops.rename(f.Name(), s.path)
	if err := s.reconcile(); err != nil {
		return err
	}
	if !bytes.Equal(s.source, next) {
		if replaceErr != nil {
			return replaceErr
		}
		return ErrConflict
	}
	return nil
}

func (s *configDocument) reconcile() error {
	data, file, err := readSettingsFile(s.path)
	if err != nil {
		return fmt.Errorf("%w: inspect config", ErrCommitUnknown)
	}
	if bytes.Equal(data, s.pending.source) && file != nil {
		// A previous rename/sync error is not success until file and directory
		// durability have both been established on the observed file.
		f, err := os.Open(s.path)
		if err != nil {
			return ErrCommitUnknown
		}
		err = f.Sync()
		err = errors.Join(err, f.Close())
		if err != nil || s.ops.syncDir(filepath.Dir(s.path)) != nil {
			return ErrCommitUnknown
		}
		verified, current, err := readSettingsFile(s.path)
		if err != nil || current == nil || !os.SameFile(file, current) || !bytes.Equal(data, verified) {
			return ErrCommitUnknown
		}
		s.source, s.file = data, current
		s.pending.publish()
		s.pending = nil
		return nil
	}
	if s.unchanged() == nil {
		s.pending = nil // Replacement demonstrably did not happen.
		return nil
	}
	return ErrCommitUnknown // Preserve evidence and block writes until reconciled.
}
