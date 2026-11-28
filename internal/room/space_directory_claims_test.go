package room

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSpaceDirectoryClaimsDeploymentPolicyAndRestart(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "hosted"
		if shared {
			name = "personal"
		}
		t.Run(name, func(t *testing.T) {
			project, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(project, "control")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			public := filepath.Join(project, "public")
			if err := os.Mkdir(public, 0700); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(home, "spaces", "b")
			directory := spaceDirectory{Spaces: []SpaceRecord{
				{ID: "a", Roots: spaceRoots{Home: home}}, {ID: "b", Roots: spaceRoots{Home: other}},
			}}
			writer := &snapshotWriter{ops: defaultSnapshotOps()}
			claims := &spaceDirectoryClaims{home: home, directory: directory, writer: writer, sharedExternal: shared}
			if err := claims.reserve("a", RoomIdentity{RoomID: "r", RoomInstance: "i"}, map[string]string{"member": public}); err != nil {
				t.Fatal(err)
			}
			restarted := &spaceDirectoryClaims{home: home, directory: directory, writer: writer, sharedExternal: shared}
			if err := restarted.restore(); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{public, filepath.Join(public, "src")} {
				if err := restarted.check("b", map[string]string{"member": path}); (err == nil) != shared {
					t.Fatalf("public directory %s: %v", path, err)
				}
			}
			for _, path := range []string{home, filepath.Join(home, "config.toml"), other} {
				if err := restarted.check("b", map[string]string{"member": path}); err == nil {
					t.Fatal("private root admitted", path)
				}
			}
			candidate := SpaceRecord{ID: "c", Roots: spaceRoots{Home: filepath.Join(public, "new-space")}}
			if err := restarted.checkNewSpaceLocked(candidate); (err == nil) != shared {
				t.Fatal("new Space inside public project", err)
			}
			// Public sharing cannot turn an already-bound directory into private state.
			restarted.saved.Claims = []spacePathClaim{{SpaceID: "a", Paths: []string{filepath.Join(candidate.Roots.Home, "existing")}}}
			if err := restarted.checkNewSpaceLocked(candidate); err == nil {
				t.Fatal("new Space absorbed an existing workspace")
			}
		})
	}
}

func TestPersonalPublicDirectoryBrowsingExcludesPrivateRootsAndAliases(t *testing.T) {
	m, project := directoryPolicyFixture(t)
	private := filepath.Join(project, "control", "spaces", "other")
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(project, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(project, "alias")); err != nil {
		t.Fatal(err)
	}
	m.workspaceProtected = []string{filepath.Join(project, "control")}
	m.config.access = &serverAccess{space: SpaceContext{SpaceID: "this"}}
	m.config.directoryClaims = &spaceDirectoryClaims{home: filepath.Join(project, "control"), sharedExternal: true,
		directory: spaceDirectory{Spaces: []SpaceRecord{{ID: "other", Roots: spaceRoots{Home: private}}}},
		saved:     spaceClaimSnapshot{Claims: []spacePathClaim{{SpaceID: "other", Paths: []string{project}}}}}
	request := httptest.NewRequest("GET", "/", nil)
	listing, err := m.browseWorkspaceDirectory(request, project, "")
	if err != nil || !listing.CanSelect {
		t.Fatal(listing, err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "src" {
		t.Fatal("private roots or aliases disclosed", listing)
	}
	for _, target := range []string{private, filepath.Join(project, "alias")} {
		if _, err := m.canonicalWorkspaceDirectory(target, false); err == nil {
			t.Fatal("private directory selectable", target)
		}
		if _, err := m.browseWorkspaceDirectory(request, target, ""); err == nil {
			t.Fatal("private directory browsable", target)
		}
	}
}
