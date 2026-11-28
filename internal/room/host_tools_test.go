package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestSharedHostToolsKeepPlatformImplementationsAndDenyRepositories(t *testing.T) {
	policy, err := deploymentHostTools("multi-user")
	if err != nil {
		t.Fatal(err)
	}
	original := defaultPlatformCatalog()
	if policy.validateCatalog(nil) == nil || policy.validateCatalog(original) != nil {
		t.Fatal("required platform declarations were not enforced")
	}
	catalog := policy.catalog(original)
	for _, provider := range []string{"gitlab", "lark"} {
		if _, ok := catalog.Types().LookupType(provider); !ok {
			t.Fatal("platform removed", provider)
		}
		if catalog.ToolRuntime(provider) == nil && original.ToolRuntime(provider) != nil {
			t.Fatal("platform runtime replaced")
		}
	}
	if _, ok := catalog.Types().LookupType("scion"); ok {
		t.Fatal("Scion admitted")
	}
	personal, _ := deploymentHostTools("personal")
	if personal.catalog(original) != original || personal.checkRepository(&workspaceConfig{Repository: &workspaceRepository{}}) != nil {
		t.Fatal("personal changed")
	}
	m := &roomManager{config: serviceConfig{hostTools: policy}}
	saved := managedRoomConfig{Workspace: &workspaceConfig{Repository: &workspaceRepository{URL: "https://gitlab.invalid/a/b.git"}}}
	if _, err := m.ensureWorkspaces(t.Context(), saved, true); err == nil {
		t.Fatal("automatic repository preparation admitted")
	}
	if _, err := m.startWorkspaceApply(workspaceApplyRequest{Target: *saved.Workspace}); err == nil {
		t.Fatal("direct apply admitted")
	}
	if _, _, _, err := m.prepareWorkspaceGitLocked(t.Context(), toolsRequest{Args: []string{"fetch"}}); err == nil {
		t.Fatal("legacy CLI admitted")
	}
	called := false
	run := platform.Execute(func(context.Context, string, map[string]string, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	for _, execute := range []platform.Execute{m.connectionExecute(RoomIdentity{}, *saved.Workspace, run), m.scionWorkspaceExecute(RoomIdentity{}, *saved.Workspace, run)} {
		if _, err := execute(t.Context(), "/", nil, "git", "status"); err == nil || called {
			t.Fatal("repository helper reached host")
		}
	}
	for _, path := range []string{"/v1/platforms/scion/auth", "/v1/computer"} {
		r := httptest.NewRecorder()
		m.ServeHTTP(r, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if r.Code == 200 {
			t.Fatal("old client admitted", path)
		}
	}
}

func TestScionSpaceAdmissionFollowsDeploymentAndCatalog(t *testing.T) {
	root := &serverAccess{space: SpaceContext{SpaceID: strings.Repeat("a", 32)}}
	for _, tc := range []struct {
		name                       string
		restricted, declared, want bool
	}{{"personal", false, true, true}, {"undeclared", false, false, false}, {"shared", true, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var catalog = defaultPlatformCatalog()
			if !tc.declared {
				catalog = nil
			}
			policy := hostToolPolicy{restricted: tc.restricted}
			child := &serverAccess{control: root, space: SpaceContext{SpaceID: strings.Repeat("b", 32)}, privateScion: policy.scionAvailable(catalog)}
			if got := !child.unavailableSpacePlatform("scion"); got != tc.want {
				t.Fatal("incorrect Space admission", got)
			}
			if !child.unavailableSpacePlatform("lark") {
				t.Fatal("Scion admission opened unrelated platform")
			}
		})
	}
}

func TestSharedWorkspaceKeepsManagedFilesWithoutHomeFallback(t *testing.T) {
	m := activationTestManager(t)
	room := managerCreate(t, m, "shared-files")
	m.config.hostTools = hostToolPolicy{restricted: true}
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	dir := filepath.Join(m.roomDir(room.RoomID), member.Workspace)
	writeFileFixture(t, filepath.Join(dir, "note.txt"), []byte("current space"))
	listing, err := m.browseMemberWorkspaceDirectory(httptest.NewRequest("GET", "/", nil), "", dir, m.roomDir(room.RoomID), member.Workspace)
	if err != nil || listing.DirectorySource != "managed" || !listing.CanSelect || len(listing.Roots) != 0 {
		t.Fatal(listing, err)
	}
	if _, err := m.canonicalWorkspaceDirectory(os.TempDir(), false); err == nil {
		t.Fatal("host HOME fallback admitted")
	}
	m.mu.Lock()
	status, err := m.workspaceStatus(room.RoomIdentity, "")
	m.mu.Unlock()
	if err != nil || status.ManagedRepositories {
		t.Fatal(status, err)
	}
	// The file boundary is retained, including links out of the admitted root.
	f, root := fileRootFixture(t)
	writeFileFixture(t, filepath.Join(root, "note.txt"), []byte("file"))
	if preview, err := f.preview(t.Context(), "note.txt"); err != nil || preview.Text != "file" {
		t.Fatal(preview, err)
	}
	if err := os.Symlink(dir, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.preview(t.Context(), "outside/note.txt"); err == nil {
		t.Fatal("link escaped file boundary")
	}
}

func TestWorkspaceBrowsingUsesCrossSpaceClaims(t *testing.T) {
	m, root := directoryPolicyFixture(t)
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	m.config.access = &serverAccess{space: SpaceContext{SpaceID: "this"}}
	m.config.directoryClaims = &spaceDirectoryClaims{saved: spaceClaimSnapshot{Claims: []spacePathClaim{{SpaceID: "other", Paths: []string{other}}}}}
	if _, err := m.canonicalWorkspaceDirectory(other, false); err == nil {
		t.Fatal("browse bypassed other space reservation")
	}
	if err := os.Symlink(other, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.canonicalWorkspaceDirectory(filepath.Join(root, "alias"), false); err == nil {
		t.Fatal("symlink bypassed other space reservation")
	}
}

func TestSharedCapacityAndHelpAreEffectiveCapabilities(t *testing.T) {
	p := hostToolPolicy{restricted: true}
	l := p.resourceLimits(executionResourceLimits{Sessions: 1})
	resources := newExecutionResources(l)
	release, err := resources.acquireSession("u", "s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resources.acquireSession("u", "s"); !errors.Is(err, ErrBusy) {
		t.Fatal("quota did not retain owner", err)
	}
	release()
	if finish, err := resources.acquireSession("u", "s"); err != nil {
		t.Fatal(err)
	} else {
		finish()
	}
	env := func(k string) string {
		if k == "TEAMX_TOOL_CONTRACT" {
			return sharedToolsContract
		}
		if k == "TEAMX_AGENT_ID" {
			return "a"
		}
		return ""
	}
	help := rootHelpText(env, defaultPlatformCatalog())
	if strings.Contains(help, "workspace-git ") || strings.Contains(help, "gitlab, lark, scion") || !strings.Contains(help, "GitLab API") {
		t.Fatal(help)
	}
	encoded, _ := json.Marshal(p.catalog(defaultPlatformCatalog()).Types())
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("capability view leaked secret")
	}
}
