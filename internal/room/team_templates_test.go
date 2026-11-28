package room

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func templateContents(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTeamDefaultsPublicationAndUserTemplates(t *testing.T) {
	source := resetSeedForTest(t)
	home, err := resolveFutureDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustWriteResetFile(t, filepath.Join(source, "seed", "role.md"), "version one")
	if err := refreshTeamDefaults(source, home); err != nil {
		t.Fatal(err)
	}
	defaults := teamDefaultsPath(filepath.Join(home, "custom.toml"))
	teams := filepath.Join(home, "teams")
	if err := initializeTeamTemplates(defaults, teams); err != nil {
		t.Fatal(err)
	}
	mustWriteResetFile(t, filepath.Join(teams, "seed", "role.md"), "user customization")
	mustWriteResetFile(t, filepath.Join(defaults, "obsolete"), "old version")
	mustWriteResetFile(t, filepath.Join(source, "seed", "role.md"), "version two")
	if err := refreshTeamDefaults(source, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(defaults, "obsolete")); !os.IsNotExist(err) {
		t.Fatal("obsolete seed survived", err)
	}
	if err := initializeTeamTemplates(defaults, teams); err != nil {
		t.Fatal(err)
	}
	if got := templateContents(t, filepath.Join(teams, "seed", "role.md")); got != "user customization" {
		t.Fatal(got)
	}
	if got := templateContents(t, filepath.Join(defaults, "seed", "role.md")); got != "version two" {
		t.Fatal(got)
	}
	// Validate the entire source before publishing anything over the last good seed.
	mustWriteResetFile(t, filepath.Join(source, "seed", "team.json"), "invalid")
	if err := refreshTeamDefaults(source, home); err == nil {
		t.Fatal("invalid seed published")
	}
	if err := requireTeamTemplates(defaults); err != nil {
		t.Fatal("previous seed damaged", err)
	}
	if err := initializeTeamTemplates(defaults, defaults); err == nil {
		t.Fatal("source used as destination")
	}
}

func TestNewSpacesUseDeploymentDefaultsAndRetainChangesOnRestart(t *testing.T) {
	home := t.TempDir()
	s, _, stop := startSpaceServerFixture(t, home)
	source := s.assembly.config.teamsSource
	mustWriteResetFile(t, filepath.Join(source, "seed", "role.md"), "deployment default")
	id := strings.Repeat("b", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "工作"}); err != nil {
		t.Fatal(err)
	}
	runtime := waitSpaceState(t, s, id, "setup_required")
	role := filepath.Join(runtime.roots.Teams, "seed", "role.md")
	if got := templateContents(t, role); got != "deployment default" {
		t.Fatal(got)
	}
	if runtime.config.teamsSource != source {
		t.Fatal("space selected its own defaults")
	}
	mustWriteResetFile(t, role, "user customization")
	stop()
	// A new deployment seed applies to future Spaces, never existing user teams.
	mustWriteResetFile(t, filepath.Join(source, "seed", "role.md"), "next version")
	restored, _, _ := startSpaceServerFixture(t, home)
	waitSpaceState(t, restored, id, "setup_required")
	if got := templateContents(t, role); got != "user customization" {
		t.Fatal(got)
	}
	nextID := strings.Repeat("c", 32)
	if _, err := restored.create(spaceChangeInput{RequestID: nextID, Name: "新工作"}); err != nil {
		t.Fatal(err)
	}
	next := waitSpaceState(t, restored, nextID, "setup_required")
	if got := templateContents(t, filepath.Join(next.roots.Teams, "seed", "role.md")); got != "next version" {
		t.Fatal(got)
	}
}

func TestTeamDefaultsRejectTemplateLinksAndResetCleanupOverlap(t *testing.T) {
	source := resetSeedForTest(t)
	home, err := resolveFutureDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshTeamDefaults(source, home); err != nil {
		t.Fatal(err)
	}
	defaults := teamDefaultsPath(filepath.Join(home, "config.toml"))
	if err := os.Symlink(source, filepath.Join(defaults, "redirect")); err != nil {
		t.Fatal(err)
	}
	if err := requireTeamTemplates(defaults); err == nil {
		t.Fatal("linked source accepted")
	}
	if err := os.Remove(filepath.Join(defaults, "redirect")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	destination := filepath.Join(home, "teams")
	if err := os.Symlink(outside, destination); err != nil {
		t.Fatal(err)
	}
	if err := initializeTeamTemplates(defaults, destination); err == nil {
		t.Fatal("linked destination accepted")
	}
	storage := &spaceResetStorage{record: SpaceRecord{Roots: spaceRoots{home, filepath.Join(home, "defaults"), filepath.Join(home, "user-teams")}}, configPath: filepath.Join(home, "config.toml"), seed: defaults, roots: map[string]resetFileIdentity{}}
	if err := storage.validate(); err == nil {
		t.Fatal("seed under deletion target accepted")
	}
}

func TestSpaceCreationMissingDefaultsDoesNotReserveIdentity(t *testing.T) {
	s, _, _ := startSpaceServerFixture(t, t.TempDir())
	if err := os.RemoveAll(s.assembly.config.teamsSource); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("d", 32)
	_, err := s.create(spaceChangeInput{RequestID: id, Name: "工作"})
	if err == nil || !strings.Contains(err.Error(), "defaults/teams") {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.directory.Spaces {
		if record.ID == id {
			t.Fatal("missing defaults reserved a Space")
		}
	}
}

func TestConfiguredTeamDefaultsIndependentOfNodeLocation(t *testing.T) {
	for _, mode := range []string{"config", "home"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, "config.toml")
			args := []string{"--home", home}
			if mode == "config" {
				config = filepath.Join(home, "development.toml")
				args = []string{"--config", config}
			}
			// No DSH process runs during setup. Its unrelated executable location must
			// have no effect on either the template source or creation of a Space.
			mustWriteResetFile(t, config, "[runtime.dsh]\nbinary_path='/opt/homebrew/bin/node'\n[models]\nschema_version=1\n")
			if err := initializeTeamTemplates(resetSeedForTest(t), teamDefaultsPath(config)); err != nil {
				t.Fatal(err)
			}
			startup, err := readStartupConfig(config, true, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareFirstAccess(startup); err != nil {
				t.Fatal(err)
			}
			selected := personalDeployment(controlledActivationFactory(nil, nil))
			_, stop := startResetServerForTest(t, args, serverOptions{deployment: &selected, platformCatalog: defaultPlatformCatalog(), probe: successfulModelProbe})
			defer stop()
			s := selected.spaces
			if s.assembly.config.teamsSource != teamDefaultsPath(config) {
				t.Fatal(s.assembly.config.teamsSource)
			}
			id := strings.Repeat("a", 32)
			if _, err := s.create(spaceChangeInput{RequestID: id, Name: "工作"}); err != nil {
				t.Fatal(err)
			}
			space := waitSpaceState(t, s, id, "setup_required")
			if got := templateContents(t, filepath.Join(space.roots.Teams, "seed", "team.json")); got != templateContents(t, filepath.Join(s.assembly.config.teamsSource, "seed", "team.json")) {
				t.Fatal("wrong initial templates")
			}
		})
	}
}

func TestRepositoryTemplatesSupportManagedModelDefaults(t *testing.T) {
	source, err := filepath.Abs("../../.teamx/teams")
	if err != nil {
		t.Fatal(err)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireTeamTemplates(source); err != nil {
		t.Fatal(err)
	}
}
