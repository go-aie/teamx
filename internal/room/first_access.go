package room

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type firstAccessRecord struct {
	Version      int    `json:"version"`
	Operator     string `json:"operator"`
	TokenPath    string `json:"token_path"`
	Completed    bool   `json:"completed"`
	LoginEnabled *bool  `json:"login_enabled,omitempty"`
}

// Caller owns the home lock. Persist the identity reservation before creating
// credentials, so an interrupted first start always resumes the same identity.
func prepareFirstAccess(c startupConfig) error {
	if err := checkDeploymentRoot(c); err != nil {
		return err
	}
	if c.Deployment.Mode == "multi-user" {
		if c.Deployment.Authentication.Provider == "" || !c.Deployment.Authentication.AllowAllAuthenticated && len(c.Deployment.Authentication.AllowedSubjects) == 0 {
			return errors.New("multi-user access requires an authentication provider and explicit admission policy")
		}
		if err := makeDurableDir(filepath.Dir(adminTokenPath(c))); err != nil {
			return err
		}
		if _, err := os.Lstat(adminTokenPath(c)); errors.Is(err, os.ErrNotExist) {
			token, err := randomAccessToken()
			if err != nil {
				return err
			}
			if err := installMissing(adminTokenPath(c), []byte(token+"\n")); err != nil {
				return err
			}
		}
		_, err := readAdminToken(adminTokenPath(c))
		return err
	}
	if c.Access.OperatorID != "" {
		if c.Access.LoginEnabled != nil && !*c.Access.LoginEnabled {
			return validateRemoteAccess(c, false)
		}
		_, err := readAdminToken(adminTokenPath(c))
		return err
	}
	// Once a User/Space reservation exists, missing access configuration is
	// damage. Do not let first-use initialization mint a replacement owner.
	for _, name := range []string{"personal-space-registration.json", "users.json", "spaces.json"} {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(c.path), name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("registered personal identity requires restoring access configuration; repair required")
		}
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
		enabled := c.Access.LoginEnabled != nil && *c.Access.LoginEnabled || remoteAccessEnabled(c)
		record = firstAccessRecord{Version: 1, Operator: "owner_" + id, TokenPath: adminTokenPath(c), LoginEnabled: &enabled}
		if err := (&snapshotWriter{ops: defaultSnapshotOps()}).save(path, record, 16<<10); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if record.Version != 1 || !validOperatorID(record.Operator) || record.TokenPath != adminTokenPath(c) || record.Completed {
		return errors.New("first-start identity is inconsistent; use access init to repair")
	}
	if record.LoginEnabled == nil || *record.LoginEnabled {
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
	}
	section, err := toml.Marshal(struct {
		Access accessConfig `toml:"access"`
	}{accessConfig{OperatorID: record.Operator, AdminTokenFile: c.Access.AdminTokenFile, LoginEnabled: record.LoginEnabled}})
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
	return (&snapshotWriter{ops: defaultSnapshotOps()}).save(path, record, 16<<10)
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

// A completed reset may intentionally retain infrastructure nested inside Data
// (for example a custom admin token). Such files do not make a new model setup
// an import of historical business state. All other entries remain forbidden.
func requireFreshModelHomeKeeping(c startupConfig, protected []string) error {
	if c.Server.DataDir == nil {
		return ErrRequest
	}
	root, err := resolveFutureDirectory(*c.Server.DataDir)
	if err != nil {
		return err
	}
	protected = append(append([]string{}, protected...), filepath.Join(root, ".lock"), filepath.Join(root, "installation-id"))
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		for _, keep := range protected {
			if pathWithin(keep, path) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			for _, keep := range protected {
				if pathWithin(path, keep) {
					return nil
				}
			}
		}
		return errors.New("existing business data requires model migration; first-use model writes are disabled")
	})
}
