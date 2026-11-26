//go:build darwin || linux

package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Inspect deployment-owned launchers and frozen Room/Session execution facts,
// including archived Spaces. Unused descriptors and user workspace files are
// not roots: retaining every descriptor would retain every release forever.
func serviceRequiredReleases(r serviceRegistration) ([]string, error) {
	releases := filepath.Join(r.Root, "releases")
	required := map[string]bool{}
	add := func(path, base string) error {
		if path == "" {
			return nil
		}
		path = configPath(base, path)
		managed := pathWithin(releases, path) || pathWithin(filepath.Join(r.Root, "current"), path)
		physical, err := filepath.EvalSymlinks(path)
		if err != nil {
			if !managed {
				return nil
			} // Non-path strings and unrelated settings.
			return fmt.Errorf("required release path %s: %w", path, err)
		}
		if !managed && !pathWithin(releases, physical) {
			return nil
		}
		rel, err := filepath.Rel(releases, physical)
		if err != nil || !pathWithin(releases, physical) || rel == "." {
			return fmt.Errorf("required path escapes releases: %s", path)
		}
		name := strings.Split(rel, string(filepath.Separator))[0]
		if err := servicePhysicalDirectory(filepath.Join(releases, name)); err != nil {
			return err
		}
		required[name] = true
		return nil
	}
	if err := add(filepath.Join(r.Root, "current"), r.Home); err != nil {
		return nil, err
	}
	var collect func(any, string) error
	collect = func(value any, base string) error {
		switch v := value.(type) {
		case string:
			return add(v, base)
		case map[string]any:
			for _, child := range v {
				if err := collect(child, base); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := collect(child, base); err != nil {
					return err
				}
			}
		}
		return nil
	}
	readJSON := func(path, base string) error {
		physical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if !pathWithin(r.Home, physical) {
			return fmt.Errorf("execution metadata outside managed home: %s", path)
		}
		var value any
		if err := readSnapshot(path, &value, maxRoomsFileBytes); err != nil {
			return err
		}
		return collect(value, base)
	}
	homes := []string{r.Home}
	var directory spaceDirectory
	identityPath := filepath.Join(r.Home, "spaces.json")
	if err := readIdentitySnapshotForUID(identityPath, &directory, uint32(r.UID)); err == nil {
		if directory.Version != 1 {
			return nil, errors.New("invalid Space directory during release retention")
		}
		for _, space := range directory.Spaces {
			for _, root := range []string{space.Roots.Home, space.Roots.Data, space.Roots.Teams} {
				if root != "" && !pathWithin(r.Home, root) {
					return nil, errors.New("external Space roots require a separate backup and retention recipe")
				}
			}
			if !slices.Contains(homes, space.Roots.Home) {
				homes = append(homes, space.Roots.Home)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read service Space directory %s (owner UID %d): %w", identityPath, r.UID, err)
	}
	for _, home := range homes {
		data, err := os.ReadFile(filepath.Join(home, "config.toml"))
		if err != nil {
			return nil, err
		}
		var document map[string]any
		if err := toml.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("invalid launcher config in %s", home)
		}
		if err := collect(document, home); err != nil {
			return nil, err
		}
		var c startupConfig
		if err := toml.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("invalid launcher config in %s", home)
		}
		if c.Deployment.Execution.Manifest != "" {
			manifest := configPath(home, c.Deployment.Execution.Manifest)
			if !pathWithin(r.Home, manifest) {
				return nil, errors.New("external execution descriptor requires a separate backup")
			}
			if err := readJSON(manifest, home); err != nil {
				return nil, err
			}
		}
		dataRoot, teams := filepath.Join(home, "data"), filepath.Join(home, "teams")
		if c.Server.DataDir != nil {
			dataRoot = configPath(home, *c.Server.DataDir)
		}
		if c.Server.TeamsDir != nil {
			teams = configPath(home, *c.Server.TeamsDir)
		}
		for _, root := range []string{dataRoot, teams} {
			real, err := resolveFutureDirectory(root)
			if err != nil {
				return nil, err
			}
			if !pathWithin(r.Home, real) {
				return nil, errors.New("external data or templates require a separate backup and retention recipe")
			}
		}
		patterns := []struct{ root, pattern string }{{teams, "*/team.json"}, {dataRoot, "rooms.json"}}
		for _, room := range []string{"", "rooms/*/"} {
			for _, suffix := range []string{"room-config.json", "agents/*/*.model.json", "agents/*/session.json.dsh*/backend.json"} {
				patterns = append(patterns, struct{ root, pattern string }{dataRoot, room + suffix})
			}
		}
		if c.Models != nil {
			for _, entry := range c.Models.Entries {
				patterns = append(patterns, struct{ root, pattern string }{home, "model-validations/" + entry.executionDigest() + ".json"})
			}
		}
		for _, pattern := range patterns {
			paths, err := serviceMetadataPaths(pattern.root, pattern.pattern)
			if err != nil {
				return nil, err
			}
			for _, path := range paths {
				if err := readJSON(path, home); err != nil {
					return nil, err
				}
			}
		}
	}
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Unlike filepath.Glob, do not hide permission/I/O errors while finding roots
// for deletion. Configured root names are literal, including any brackets.
func serviceMetadataPaths(root, pattern string) ([]string, error) {
	paths := []string{root}
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		var next []string
		for _, path := range paths {
			entries, err := os.ReadDir(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				matches, err := filepath.Match(part, entry.Name())
				if err != nil {
					return nil, err
				}
				if !matches {
					continue
				}
				if i < len(parts)-1 && !entry.IsDir() {
					if entry.Type()&os.ModeSymlink == 0 && strings.ContainsAny(part, "*?[") {
						continue
					}
					return nil, fmt.Errorf("execution metadata directory is not physical: %s", filepath.Join(path, entry.Name()))
				}
				next = append(next, filepath.Join(path, entry.Name()))
			}
		}
		paths = next
	}
	return paths, nil
}

func servicePhysicalDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || physical != path {
		return fmt.Errorf("expected physical directory: %s", path)
	}
	return nil
}

// Only successful deployments explicitly enrolled by the runner are eligible
// for automatic deletion. Manual backups, other installations and failed jobs
// are never discovered by directory name or modification time.
type serviceRetention struct {
	Version          int      `json:"version"`
	InstallationID   string   `json:"installation_id"`
	Releases         []string `json:"releases"`
	Backups          []string `json:"backups"`
	DeletingReleases []string `json:"deleting_releases,omitempty"`
	DeletingBackups  []string `json:"deleting_backups,omitempty"`
}

type servicePruneResult struct {
	RetainedReleases []string `json:"retained_releases"`
	DeletedReleases  []string `json:"deleted_releases"`
	RetainedBackups  []string `json:"retained_backups"`
	DeletedBackups   []string `json:"deleted_backups"`
}

func pruneServiceDeployment(r serviceRegistration, backup string, keepReleases, keepBackups int) (servicePruneResult, error) {
	return pruneServiceDeploymentContext(context.Background(), r, backup, keepReleases, keepBackups)
}

func pruneServiceDeploymentContext(ctx context.Context, r serviceRegistration, backup string, keepReleases, keepBackups int) (servicePruneResult, error) {
	var result servicePruneResult
	if keepReleases < 2 || keepBackups < 1 {
		return result, errors.New("keep-releases must be at least 2 and keep-backups at least 1")
	}
	path := filepath.Join(r.Root, "retention.json")
	state := serviceRetention{Version: 1, InstallationID: r.InstallationID}
	if err := readSnapshot(path, &state, maxPrivateFileBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if state.Version != 1 || state.InstallationID != r.InstallationID {
		return result, errors.New("retention state belongs to another installation")
	}
	current, err := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	if err != nil {
		return result, err
	}
	if filepath.Dir(current) != filepath.Join(r.Root, "releases") {
		return result, errors.New("invalid current release")
	}
	previous := ""
	verified := map[string]bool{}
	programObjects := map[string][]os.FileInfo{}
	if backup != "" {
		if err := servicePhysicalDirectory(backup); err != nil {
			return result, err
		}
		b, err := verifyServiceBackupContext(ctx, backup, r)
		if err != nil {
			return result, err
		}
		if err := verifyServiceBackupProgramContent(ctx, backup, b, programObjects); err != nil {
			return result, err
		}
		previous = b.Current
		verified[backup] = true
		state.Backups = append([]string{backup}, withoutServiceValue(state.Backups, backup)...)
	}
	state.Releases = append([]string{filepath.Base(current)}, withoutServiceValue(state.Releases, filepath.Base(current))...)
	if previous != "" && previous != filepath.Base(current) {
		state.Releases = append(state.Releases[:1], append([]string{previous}, withoutServiceValue(state.Releases[1:], previous)...)...)
	}
	required, err := serviceRequiredReleases(r)
	if err != nil {
		return result, err
	}
	keep := map[string]bool{}
	for _, name := range required {
		keep[name] = true
	}
	for i, name := range state.Releases {
		if name == "." || name == ".." || name == "" || filepath.Base(name) != name {
			return result, errors.New("invalid retained release name")
		}
		if i < keepReleases {
			keep[name] = true
		}
	}
	entries, err := os.ReadDir(filepath.Join(r.Root, "releases"))
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if keep[name] {
			result.RetainedReleases = append(result.RetainedReleases, name)
			continue
		}
		// Only published, managed releases are candidates; leave partial work and
		// unknown/symlink entries for explicit diagnosis.
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		var manifest struct {
			Profile string `json:"profile"`
		}
		if !slices.Contains(state.DeletingReleases, name) {
			b, err := os.ReadFile(filepath.Join(r.Root, "releases", name, "manifest.json"))
			if err != nil || json.Unmarshal(b, &manifest) != nil || manifest.Profile != "standard" {
				continue
			}
		}
		if err := servicePhysicalDirectory(filepath.Join(r.Root, "releases", name)); err != nil {
			return result, err
		}
		result.DeletedReleases = append(result.DeletedReleases, name)
	}
	for i, name := range state.Backups {
		if !filepath.IsAbs(name) || filepath.Clean(name) != name || pathWithin(r.Root, name) || pathWithin(r.Home, name) || pathWithin(name, r.Root) || pathWithin(name, r.Home) {
			return result, errors.New("invalid retained backup path")
		}
		for _, other := range state.Backups[:i] {
			if pathWithin(name, other) || pathWithin(other, name) {
				return result, errors.New("retained backup paths overlap")
			}
		}
		if err := servicePhysicalDirectory(name); errors.Is(err, os.ErrNotExist) && i >= keepBackups {
			// A prior cleanup may have removed this old backup before its final
			// state rewrite. Missing backups inside the retained set still fail.
			continue
		} else if err != nil {
			return result, err
		}
		if i >= keepBackups && slices.Contains(state.DeletingBackups, name) {
			// The published deletion plan already checked this backup's identity.
			// Its manifest may have been removed by an interrupted RemoveAll.
			result.DeletedBackups = append(result.DeletedBackups, name)
			continue
		}
		var b serviceBackup
		if err := readSnapshot(filepath.Join(name, "backup.json"), &b, maxServiceBackupManifestBytes); err != nil {
			return result, err
		}
		if (b.Version != 1 && b.Version != 2 && b.Version != 3) || b.Registration.Root != r.Root || b.Registration.Home != r.Home || b.Registration.InstallationID != r.InstallationID || b.Registration.UID != r.UID {
			return result, errors.New("retained backup belongs to another installation")
		}
		if i < keepBackups {
			if !verified[name] {
				checked, err := verifyServiceBackupContext(ctx, name, r)
				if err != nil {
					return result, err
				}
				if err := verifyServiceBackupProgramContent(ctx, name, checked, programObjects); err != nil {
					return result, err
				}
			}
			result.RetainedBackups = append(result.RetainedBackups, name)
		} else {
			result.DeletedBackups = append(result.DeletedBackups, name)
		}
	}
	// Persist successful deployment history before deletion. An interrupted
	// cleanup can retry; missing entries are removed below only after success.
	state.DeletingReleases = result.DeletedReleases
	state.DeletingBackups = result.DeletedBackups
	if err := writeServiceJSON(path, state); err != nil {
		return result, err
	}
	if err := syncDirectory(r.Root); err != nil {
		return result, err
	}
	for _, name := range result.DeletedReleases {
		if err := os.RemoveAll(filepath.Join(r.Root, "releases", name)); err != nil {
			return result, err
		}
	}
	for _, name := range result.DeletedBackups {
		if err := os.RemoveAll(name); err != nil {
			return result, err
		}
	}
	if err := pruneServiceProgramObjects(r, result.DeletedReleases, state.Backups); err != nil {
		return result, err
	}
	state.Releases = state.Releases[:min(len(state.Releases), keepReleases)]
	state.Backups = result.RetainedBackups
	state.DeletingReleases, state.DeletingBackups = nil, nil
	if err := writeServiceJSON(path, state); err != nil {
		return result, err
	}
	return result, syncDirectory(r.Root)
}

func withoutServiceValue(values []string, value string) []string {
	result := make([]string, 0, len(values))
	for _, v := range values {
		if v != value {
			result = append(result, v)
		}
	}
	return result
}
