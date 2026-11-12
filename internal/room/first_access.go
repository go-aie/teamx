package room

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type firstAccessRecord struct {
	Version   int    `json:"version"`
	Operator  string `json:"operator"`
	TokenPath string `json:"token_path"`
	Completed bool   `json:"completed"`
}

// Caller owns the home lock. Persist the identity reservation before creating
// credentials, so an interrupted first start always resumes the same identity.
func prepareFirstAccess(c startupConfig) error {
	if c.Access.OperatorID != "" {
		_, err := readAdminToken(adminTokenPath(c))
		return err
	}
	path := filepath.Join(filepath.Dir(c.path), "first-access.json")
	var record firstAccessRecord
	err := readSnapshot(path, &record, 16<<10)
	if errors.Is(err, os.ErrNotExist) {
		if err := requireFreshModelHome(c); err != nil {
			return fmt.Errorf("automatic authentication requires a new home; use teamx access init --config %s to repair existing access", c.path)
		}
		if _, err := os.Lstat(adminTokenPath(c)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("unregistered authentication exists; use access init to repair")
		}
		if _, err := os.Lstat(filepath.Join(*c.Server.DataDir, "installation-id")); !errors.Is(err, os.ErrNotExist) {
			return errors.New("existing installation requires access init to repair")
		}
		id, err := newOpaqueID()
		if err != nil {
			return err
		}
		record = firstAccessRecord{Version: 1, Operator: "owner_" + id, TokenPath: adminTokenPath(c)}
		if err := (&Store{ops: defaultSnapshotOps()}).save(path, record, 16<<10); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if record.Version != 1 || !validOperatorID(record.Operator) || record.TokenPath != adminTokenPath(c) || record.Completed {
		return errors.New("first-start identity is inconsistent; use access init to repair")
	}
	if err := makeDurableDir(filepath.Dir(record.TokenPath)); err != nil {
		return err
	}
	if _, err := os.Lstat(record.TokenPath); errors.Is(err, os.ErrNotExist) {
		token, err := randomAccessToken()
		if err != nil {
			return err
		}
		if err := installMissing(record.TokenPath, []byte(token+"\n")); err != nil {
			return err
		}
	}
	if _, err := readAdminToken(record.TokenPath); err != nil {
		return err
	}
	section, err := toml.Marshal(struct {
		Access accessConfig `toml:"access"`
	}{accessConfig{OperatorID: record.Operator, AdminTokenFile: c.Access.AdminTokenFile}})
	if err != nil {
		return err
	}
	source, err := removeConfigSubtrees(c.source, []string{"access"})
	if err != nil {
		return err
	}
	next := append(append(source, '\n'), section...)
	if err := writePrivateConfig(c.path, c.source, next); err != nil {
		return err
	}
	record.Completed = true
	return (&Store{ops: defaultSnapshotOps()}).save(path, record, 16<<10)
}

func requireFreshModelHome(c startupConfig) error {
	if c.Server.DataDir == nil {
		return ErrRequest
	}
	entries, err := os.ReadDir(*c.Server.DataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" && entry.Name() != "installation-id" {
			return errors.New("existing business data requires model migration; first-use model writes are disabled")
		}
	}
	return nil
}
