//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestServicePruneRequiresAuthenticatedRunningInstallation(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(map[bool]string{true: "running", false: "stopped"}[running], func(t *testing.T) {
			r, server := serviceStartupFixture(t, nil)
			unused := serviceRetentionRelease(t, r, "dev-unused")
			if !running {
				server.Close()
			}
			var stdout, stderr bytes.Buffer
			err := runService([]string{"prune", "--server-root", r.Root}, &stdout, &stderr)
			if !running {
				if err == nil {
					t.Fatal("pruned a stopped service")
				}
				if _, err := os.Stat(unused); err != nil {
					t.Fatal("deleted before authentication", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var result servicePruneResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(result.DeletedReleases, filepath.Base(unused)) {
				t.Fatal(result)
			}
		})
	}
}

func serviceRetentionRelease(t *testing.T, r serviceRegistration, version string) string {
	t.Helper()
	archive, digest := serviceTestArchive(t, serviceTestRelease(t, version))
	release, err := publishServiceArchive(r.Root, archive, digest)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestServiceReleaseReferencesRequireRegisteredIdentityOwner(t *testing.T) {
	for _, scenario := range []string{"registered-owner", "wrong-owner", "permissions", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			r := serviceTestRegistration(t)
			path := filepath.Join(r.Home, "spaces.json")
			if err := writeServiceJSON(path, spaceDirectory{Version: 1}); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "wrong-owner":
				r.UID++
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("spaces.json.saved", path); err != nil {
					t.Fatal(err)
				}
			}
			_, err := serviceRequiredReleases(r)
			if scenario == "registered-owner" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("accepted identity file outside registered ownership/permissions", scenario)
			}
		})
	}
}

func TestServiceBackupArchivesAllReleasesAndRestoresIndependently(t *testing.T) {
	r := serviceTestRegistration(t)
	original, _ := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	unused := serviceRetentionRelease(t, r, "dev-unused")
	pinned := serviceRetentionRelease(t, r, "dev-space")
	frozen := serviceRetentionRelease(t, r, "dev-session")
	spaceHome := filepath.Join(r.Home, "spaces", "archived")
	serviceTestFile(t, filepath.Join(spaceHome, "config.toml"), "[runtime.dsh]\nbinary_path = '"+filepath.Join(pinned, "bin/dsh")+"'\n", 0600)
	directory := spaceDirectory{Version: 1, Spaces: []SpaceRecord{{ID: "archived", Status: "archived", Roots: spaceRoots{Home: spaceHome}}}}
	if err := writeServiceJSON(filepath.Join(r.Home, "spaces.json"), directory); err != nil {
		t.Fatal(err)
	}
	serviceTestFile(t, filepath.Join(r.Home, "data/rooms/room/agents/member/session.json.dsh.model.json"), `{"runtime":{"binary_path":"`+filepath.Join(frozen, "bin/dsh")+`"}}`, 0600)
	serviceTestFile(t, filepath.Join(r.Home, "execution", filepath.Base(unused)+".json"), `{"runtime":{"path":"`+filepath.Join(unused, "runtime/srt")+`"}}`, 0600)
	backup := filepath.Join(filepath.Dir(r.Root), "selected-backup")
	if err := createServiceBackup(r, backup); err != nil {
		t.Fatal(err)
	}
	b, err := verifyServiceBackup(backup, r)
	if err != nil {
		t.Fatal(err)
	}
	extracted := t.TempDir()
	if err := readServiceBackupArchive(context.Background(), backup, b, extracted); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(extracted, "server", "releases"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	want := []string{filepath.Base(original), filepath.Base(pinned), filepath.Base(frozen), filepath.Base(unused)}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backed up releases %v; want %v", got, want)
	}
	// Restore independently, after all required installed releases disappeared.
	for _, release := range []string{original, pinned, frozen, unused} {
		if err := os.RemoveAll(release); err != nil {
			t.Fatal(err)
		}
	}
	if err := restoreServiceBackup(context.Background(), r, backup); err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{original, pinned, frozen, unused} {
		if _, err := os.Stat(filepath.Join(release, "bin/dsh")); err != nil {
			t.Fatal("release unavailable after restore", err)
		}
	}
}

func TestServiceBackupRestoresLegacyFullReleaseInventory(t *testing.T) {
	r := serviceTestRegistration(t)
	unused := serviceRetentionRelease(t, r, "dev-unused")
	backup := filepath.Join(filepath.Dir(r.Root), "legacy-full-backup")
	createLegacyServiceTestBackup(t, r, backup)
	if err := os.RemoveAll(filepath.Join(r.Root, "releases")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.Root, "releases"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := restoreServiceBackup(context.Background(), r, backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(unused, "bin/dsh")); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRetentionKeepsPreviousVersionPinnedDependenciesAndTwoBackups(t *testing.T) {
	r := serviceTestRegistration(t)
	original, _ := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	unused := serviceRetentionRelease(t, r, "dev-unused")
	// A frozen backend is a dependency even when no Space launcher names it.
	serviceTestFile(t, filepath.Join(r.Home, "data/rooms/room/agents/member/session.json.dsh-rotated/backend.json"), `{"binaryPath":"`+filepath.Join(original, "bin/dsh")+`"}`, 0600)
	manual := filepath.Join(filepath.Dir(r.Root), "manual-backup")
	failed := filepath.Join(filepath.Dir(r.Root), "failed-deployment-backup")
	for _, path := range []string{manual, failed} {
		if err := createServiceBackup(r, path); err != nil {
			t.Fatal(err)
		}
	}
	var backups, versions []string
	for _, version := range []string{"dev-b", "dev-c", "dev-d"} {
		backup := filepath.Join(filepath.Dir(r.Root), "before-"+version)
		if err := createServiceBackup(r, backup); err != nil {
			t.Fatal(err)
		}
		backups = append(backups, backup)
		release := serviceRetentionRelease(t, r, version)
		versions = append(versions, release)
		if err := selectServiceRelease(r.Root, release); err != nil {
			t.Fatal(err)
		}
		if _, err := pruneServiceDeployment(r, backup, 2, 2); err != nil {
			t.Fatal(err)
		}
	}
	for _, retained := range []string{original, versions[1], versions[2], backups[1], backups[2], manual, failed} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatal("deleted retained material", retained, err)
		}
	}
	for _, deleted := range []string{unused, versions[0], backups[0]} {
		if _, err := os.Stat(deleted); !os.IsNotExist(err) {
			t.Fatal("did not remove obsolete material", deleted, err)
		}
	}
	// A later manual restoration may re-publish an old version from its own copy.
	if err := restoreServiceBackup(context.Background(), r, manual); err != nil {
		t.Fatal(err)
	}
	if current, _ := filepath.EvalSymlinks(filepath.Join(r.Root, "current")); current != original {
		t.Fatal(current)
	}
}

func TestServiceRetentionRejectsUncertainReferencesOrRecoveryMaterialBeforeDeletion(t *testing.T) {
	for _, scenario := range []string{"missing-runtime", "corrupt-backup", "foreign-state", "symlink-backup", "unsafe-count"} {
		t.Run(scenario, func(t *testing.T) {
			r := serviceTestRegistration(t)
			unused := serviceRetentionRelease(t, r, "dev-unused")
			backup := filepath.Join(filepath.Dir(r.Root), "before-upgrade")
			if err := createServiceBackup(r, backup); err != nil {
				t.Fatal(err)
			}
			keep := 2
			switch scenario {
			case "missing-runtime":
				serviceTestFile(t, filepath.Join(r.Home, "data/room-config.json"), `{"runtime":{"binary_path":"`+filepath.Join(r.Root, "releases/missing/bin/dsh")+`"}}`, 0600)
			case "corrupt-backup":
				serviceTestFile(t, filepath.Join(backup, serviceBackupArchive), "damaged", 0600)
			case "foreign-state":
				if err := writeServiceJSON(filepath.Join(r.Root, "retention.json"), serviceRetention{Version: 1, InstallationID: "foreign"}); err != nil {
					t.Fatal(err)
				}
			case "symlink-backup":
				link := backup + "-link"
				if err := os.Symlink(backup, link); err != nil {
					t.Fatal(err)
				}
				backup = link
			case "unsafe-count":
				keep = 1
			}
			if _, err := pruneServiceDeployment(r, backup, keep, 2); err == nil {
				t.Fatal("accepted unsafe retention")
			}
			if _, err := os.Stat(unused); err != nil {
				t.Fatal("deleted release before validating recovery", err)
			}
			if scenario == "missing-runtime" {
				dest := filepath.Join(filepath.Dir(r.Root), "invalid-backup")
				if err := createServiceBackup(r, dest); err == nil {
					t.Fatal("published incomplete backup")
				}
				if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Fatal("invalid backup published", err)
				}
			}
		})
	}
}

func TestServiceRetentionInterruptedCleanupCanResume(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "partial"}[partial], func(t *testing.T) {
			r := serviceTestRegistration(t)
			var backups []string
			for _, name := range []string{"new", "middle", "old"} {
				backup := filepath.Join(filepath.Dir(r.Root), name)
				if err := createServiceBackup(r, backup); err != nil {
					t.Fatal(err)
				}
				backups = append(backups, backup)
			}
			unused := serviceRetentionRelease(t, r, "dev-unused")
			state := serviceRetention{Version: 1, InstallationID: r.InstallationID, Backups: backups, DeletingBackups: backups[2:], DeletingReleases: []string{filepath.Base(unused)}}
			if err := writeServiceJSON(filepath.Join(r.Root, "retention.json"), state); err != nil {
				t.Fatal(err)
			}
			// Simulate a crash after deletion but before the state rewrite.
			old := backups[2]
			if partial {
				old = filepath.Join(old, "backup.json")
			}
			if err := os.RemoveAll(old); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(unused, "manifest.json")); err != nil {
				t.Fatal(err)
			}
			if _, err := pruneServiceDeployment(r, "", 2, 2); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(r.Root, "retention.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(state.Backups, backups[:2]) {
				t.Fatal(state.Backups)
			}
			for _, deleted := range []string{unused, backups[2]} {
				if _, err := os.Stat(deleted); !os.IsNotExist(err) {
					t.Fatal("interrupted removal not finished", deleted, err)
				}
			}
		})
	}
}
