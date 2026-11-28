package room

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Initial templates belong to the deployment, independent of its execution runtime.
func teamDefaultsPath(configPath string) string {
	home := filepath.Dir(configPath)
	if canonical, err := resolveFutureDirectory(home); err == nil {
		home = canonical
	}
	return filepath.Join(home, "defaults", "teams")
}

func requireTeamTemplates(source string) error {
	canonical, err := resolveFutureDirectory(source)
	if err == nil && canonical == filepath.Clean(source) {
		err = validateTeamTemplates(source)
	} else if err == nil {
		err = ErrInvalid
	}
	if err != nil {
		return &requestDiagnostic{cause: ErrRequest, Message: fmt.Sprintf("初始团队模板不可用：%s。请准备部署配置目录下的 defaults/teams。", source)}
	}
	return nil
}

// Initialization is retryable and never replaces an existing user file.
func initializeTeamTemplates(source, destination string) error {
	if err := requireTeamTemplates(source); err != nil {
		return err
	}
	canonical, err := resolveFutureDirectory(destination)
	if err != nil {
		return err
	}
	// Home may be reached through a system alias (for example /var on macOS).
	// The template directory itself must never redirect initialization.
	for _, path := range []string{destination, filepath.Dir(destination)} {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
	}
	destination = canonical
	if absoluteDirectoriesOverlap(source, destination) {
		return ErrInvalid
	}
	if err := makeDurableDir(destination); err != nil {
		return err
	}
	info, err := os.Stat(destination)
	if err != nil {
		return err
	}
	id, err := resetIdentity(info)
	if err != nil {
		return err
	}
	return copyTeamTemplates(source, destination, id, true)
}

// Called while the deployment Home lock is held, before serving any Space.
// Publish a complete version seed; user teams are never part of this replacement.
func refreshTeamDefaults(source, home string) error {
	if err := requireTeamTemplates(source); err != nil {
		return err
	}
	destination := teamDefaultsPath(filepath.Join(home, "config.toml"))
	canonical, err := resolveFutureDirectory(destination)
	if err != nil {
		return err
	}
	if canonical != destination || absoluteDirectoriesOverlap(source, destination) {
		return ErrInvalid
	}
	parent := filepath.Dir(destination)
	if err := makeDurableDir(parent); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".teams-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := initializeTeamTemplates(source, stage); err != nil {
		return err
	}
	// The previous directory is retained until the prepared tree is published.
	previous := filepath.Join(parent, ".teams-previous")
	if err := os.RemoveAll(previous); err != nil {
		return err
	}
	if err := os.Rename(destination, previous); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, destination); err != nil {
		return errors.Join(err, os.Rename(previous, destination))
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	return os.RemoveAll(previous)
}

func validateTeamTemplates(source string) error {
	count := 0
	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
			return ErrInvalid
		}
		if d.Name() == "team.json" {
			count++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrInvalid
	}
	defaults := &teamRuntime{ManagedModels: true}
	defaults.DSH.BinaryPath = "/reset-template-check"
	teams, err := loadTeams(source, defaults)
	if err != nil {
		return err
	}
	if len(teams) == 0 {
		return ErrInvalid
	}
	return nil
}
func copyTeamTemplates(source, destination string, expected resetFileIdentity, preserveExisting bool) error {
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	id, err := resetIdentity(info)
	if err != nil || id != expected {
		return resetPathError()
	}
	// Only this initializer creates these top-level temporary template files.
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !preserveExisting && strings.HasPrefix(entry.Name(), ".init-") {
			if entry.IsDir() {
				return ErrInvalid
			}
			if err := root.Remove(entry.Name()); err != nil {
				return err
			}
		}
	}
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		if d.IsDir() {
			return root.MkdirAll(rel, 0700)
		}
		if !d.Type().IsRegular() {
			return ErrInvalid
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if info, err := root.Lstat(rel); err == nil {
			if !info.Mode().IsRegular() {
				return ErrInvalid
			}
			if preserveExisting {
				return nil
			}
			f, err := root.Open(rel)
			if err != nil {
				return err
			}
			opened, err := f.Stat()
			if err != nil || !os.SameFile(info, opened) {
				f.Close()
				return ErrConflict
			}
			actual, err := io.ReadAll(io.LimitReader(f, int64(len(data))+1))
			if err == nil {
				err = f.Sync()
			}
			err = errors.Join(err, f.Close())
			if err != nil {
				return err
			}
			if !bytes.Equal(actual, data) {
				return fmt.Errorf("team template differs: %w", ErrConflict)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			id, err := newOpaqueID()
			if err != nil {
				return err
			}
			tmp := ".init-" + id
			f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			defer root.Remove(tmp)
			_, writeErr := f.Write(data)
			if writeErr == nil {
				writeErr = f.Sync()
			}
			writeErr = errors.Join(writeErr, f.Close())
			if writeErr != nil {
				return writeErr
			}
			if err := root.Link(tmp, rel); err != nil {
				return err
			}
			if err := root.Remove(tmp); err != nil {
				return err
			}
		} else {
			return err
		}
		parent, err := root.Open(filepath.Dir(rel))
		if err != nil {
			return err
		}
		if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
			return err
		}
		return syncResetRoot(root)
	})
}
