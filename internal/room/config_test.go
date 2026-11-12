package room

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeStartupConfig(t *testing.T, dir, text string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultStartupConfigUsesUserDirectory(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	writeStartupConfig(t, project, "invalid current-directory config")
	if err := os.Mkdir(filepath.Join(project, ".teamx"), 0700); err != nil {
		t.Fatal(err)
	}
	writeStartupConfig(t, filepath.Join(project, ".teamx"), "invalid project config")
	t.Chdir(project)
	path, err := defaultStartupConfigPath()
	if err != nil || path != filepath.Join(homeDir, ".teamx", "config.toml") {
		t.Fatal("default config depended on working directory", path, err)
	}
}

func TestMissingOptionalConfigKeepsDataBesideConfig(t *testing.T) {
	base := filepath.Join(t.TempDir(), ".teamx")
	config, err := loadStartupConfig(filepath.Join(base, "config.toml"), false)
	if err != nil || *config.Server.DataDir != filepath.Join(base, "data") || *config.Server.TeamsDir != filepath.Join(base, "teams") {
		t.Fatal("missing optional config fell back to working directory", err)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("config lookup created runtime data", err)
	}
}

func TestRetiredDataDirectoryNamesAreRejected(t *testing.T) {
	path := writeStartupConfig(t, t.TempDir(), "[server]\nrooms_dir = './old'\n")
	if _, err := loadStartupConfig(path, true); err == nil {
		t.Fatal("retired TOML field accepted")
	}
	var stdout, stderr bytes.Buffer
	for _, name := range []string{"rooms-dir", "data-dir", "teams-dir"} {
		if err := serve([]string{"--" + name, "old"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") || stdout.Len() != 0 {
			t.Fatal("directory command-line flag accepted", name, err)
		}
	}
}

func TestSingleRoomEmptyDataDirectoryDoesNotFallBackToCWD(t *testing.T) {
	path := writeStartupConfig(t, t.TempDir(), "[server]\ndata_dir = ''\n")
	var stdout, stderr bytes.Buffer
	if err := serve([]string{"--config", path, "--room", "test"}, &stdout, &stderr); !errors.Is(err, ErrRequest) || stdout.Len() != 0 {
		t.Fatal("empty data directory was accepted", err)
	}
}

func TestStartupConfigExampleAndPaths(t *testing.T) {
	data, err := os.ReadFile("../../.teamx/config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Exercise paths independently of the shell's working directory.
	text := strings.Replace(string(data), "/absolute/path/to/dsh", "./runtime/dsh", 1)
	text = strings.Replace(text, "# binary_args =", "binary_args =", 1)
	text = strings.Replace(text, "/absolute/path/to/dsh/lib/bin.js", "./runtime/cli.mjs", 1)
	text += "\nTEAMX_GLAB_BIN = './bin/glab'\nTEAMX_GITLAB_BIN = './bin/teamx-gitlab'\n"
	c, err := loadStartupConfig(writeStartupConfig(t, dir, text), true)
	if err != nil {
		t.Fatal(err)
	}
	if *c.Server.DataDir != filepath.Join(dir, "data") || *c.Server.TeamsDir != filepath.Join(dir, "teams") ||
		c.Runtime.DSH.BinaryPath != filepath.Join(dir, "runtime/dsh") || c.Runtime.DSH.BinaryArgs[0] != filepath.Join(dir, "runtime/cli.mjs") ||
		c.Env["TEAMX_GLAB_BIN"] != filepath.Join(dir, "bin/glab") || c.Env["TEAMX_GITLAB_BIN"] != filepath.Join(dir, "bin/teamx-gitlab") ||
		c.Models == nil || c.Models.SchemaVersion != 1 || len(c.Models.Entries) != 0 || c.Models.Default != "" || c.Runtime.Model.ModelID != "" || c.Runtime.Model.APIKeyEnv != "" {
		t.Fatal("startup values or config-relative paths lost")
	}
	if _, err := loadStartupConfig(filepath.Join(dir, "absent.toml"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStartupConfig(filepath.Join(dir, "absent.toml"), true); err == nil {
		t.Fatal("explicit missing config accepted")
	}
	empty, err := loadStartupConfig(writeStartupConfig(t, dir, "# use default directories\n"), true)
	if err != nil || *empty.Server.DataDir != filepath.Join(dir, "data") || *empty.Server.TeamsDir != filepath.Join(dir, "teams") {
		t.Fatal("default directories did not follow config location", err)
	}
}

func TestStartupConfigRejectsInvalidInputWithoutSecrets(t *testing.T) {
	const secret = "private-config-value-never-print"
	for _, text := range []string{
		`[env]` + "\nKEY = " + secret,
		`[env]` + "\nKEY = \"" + secret + "\"\n[server]\nunknown = \"" + secret + "\"",
		"[server]\nsession_max_turns = \"" + secret + "\"",
		"[env]\nKEY = 123",
		"[env]\nKEY = \"first\"\nKEY = \"second\"",
		"[env]\nTEAMX_AGENT_ID = \"forged\"",
		"[env]\nTEAMX_CLI = \"forged\"",
		"[env]\n\"invalid=key\" = \"value\"",
		"[env]\nKEY = \"\\u0000\"",
		"[runtime.model]\nmodel_id = \"" + secret + "\"",
		strings.Repeat("#", (1<<20)+1),
	} {
		_, err := loadStartupConfig(writeStartupConfig(t, t.TempDir(), text), true)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("invalid config accepted or credential echoed")
		}
	}
}

func TestStartupEnvironmentPrecedenceAndCleanup(t *testing.T) {
	const key = "TEAMX_TEST_CONFIG_SECRET"
	t.Setenv(key, "external")
	for _, value := range []string{"external", ""} {
		if err := os.Setenv(key, value); err != nil {
			t.Fatal(err)
		}
		restore, err := applyStartupEnv(map[string]string{key: "from-file"})
		if err != nil || os.Getenv(key) != value {
			t.Fatal("exported environment did not win")
		}
		restore()
	}
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	restore, err := applyStartupEnv(map[string]string{key: "from-file"})
	if err != nil || os.Getenv(key) != "from-file" {
		t.Fatal("config environment unavailable")
	}
	restore()
	if _, exists := os.LookupEnv(key); exists {
		t.Fatal("environment leaked after service shutdown")
	}
}

func TestStartupServerSettingsPrecedence(t *testing.T) {
	config, err := loadStartupConfig(writeStartupConfig(t, t.TempDir(), "[server]\nlisten = '127.0.0.1:8089'\ndata_dir = './saved'\nteams_dir = './catalog'\nsession_max_turns = 9\nmerge_window = '250ms'\ndsh_turn_timeout = '30m'\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		f := flag.NewFlagSet("serve", flag.ContinueOnError)
		listen := f.String("listen", "127.0.0.1:8088", "")
		turns := f.Uint64("session-max-turns", 16, "")
		merge := f.Duration("merge-window", 0, "")
		timeout := f.Duration("dsh-turn-timeout", 15*time.Minute, "")
		args := []string{}
		if explicit {
			args = []string{"--listen", "127.0.0.1:0", "--session-max-turns", "7", "--merge-window", "0s", "--dsh-turn-timeout", "20m"}
		}
		if err := f.Parse(args); err != nil {
			t.Fatal(err)
		}
		visited := map[string]bool{}
		f.Visit(func(flag *flag.Flag) { visited[flag.Name] = true })
		if err := applyStartupSettings(f, visited, config); err != nil {
			t.Fatal(err)
		}
		if explicit {
			if *listen != "127.0.0.1:0" || *turns != 7 || *merge != 0 || *timeout != 20*time.Minute {
				t.Fatal("explicit flags, including zero duration, did not win")
			}
		} else if *listen != *config.Server.Listen || *turns != 9 || *merge != 250*time.Millisecond || *timeout != 30*time.Minute {
			t.Fatal("config did not override built-in defaults")
		}
	}
}

func TestServeRejectsInvalidDSHTurnTimeout(t *testing.T) {
	for _, value := range []string{"0s", "-1m", "invalid", "999999999999h"} {
		for _, source := range []string{"config", "flag"} {
			t.Run(source+"/"+value, func(t *testing.T) {
				config := ""
				if source == "config" {
					config = "[server]\ndsh_turn_timeout = '" + value + "'\n"
				}
				args := []string{"--config", writeStartupConfig(t, t.TempDir(), config)}
				if source == "flag" {
					args = append(args, "--dsh-turn-timeout", value)
				}
				var stdout, stderr bytes.Buffer
				err := serveWithFactory(args, &stdout, &stderr, func(string, string, serviceConfig) (*service, error) {
					t.Fatal("invalid timeout reached service creation")
					return nil, nil
				})
				if err == nil || !strings.Contains(err.Error(), "dsh-turn-timeout") || stdout.Len() != 0 {
					t.Fatal("invalid turn timeout accepted", err)
				}
			})
		}
	}
}

func TestGlobalRuntimeDefaultsOverridesAndFrozenRooms(t *testing.T) {
	root, teams := t.TempDir(), t.TempDir()
	defaults := testTeamDefinition(t).Runtime
	defaults.Model.InputModalities = []string{"text", "image"}
	developerRole := false
	defaults.Model.Compat.SupportsDeveloperRole = &developerRole
	team := testTeamDefinition(t)
	team.Runtime = nil
	writeTestTeam(t, teams, team)
	if _, err := loadTeams(teams, nil); err == nil {
		t.Fatal("missing runtime accepted without default")
	}
	m, err := newTeamRoomManager(root, teams, 4, defaults)
	if err != nil {
		t.Fatal(err)
	}
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0], dshTurnTimeout: 15 * time.Minute}
	room := createTeamRoom(t, m, "global-runtime")
	saved, _ := readManagedRoom(t, m, room)
	if saved.Runtime == nil || !reflect.DeepEqual(saved.Runtime.Model, defaults.Model) || saved.Runtime.DSH.BinaryPath != defaults.DSH.BinaryPath {
		t.Fatal("resolved runtime not frozen")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	defaults.Model.ModelID = "changed-global-model"
	developerRole = true
	m, err = newTeamRoomManager(root, teams, 4, defaults)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0], dshTurnTimeout: 30 * time.Minute}
	cfg, err := m.activationConfig(room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dshTurnTimeout != 30*time.Minute {
		t.Fatal("existing Room did not pick up current service timeout", cfg.dshTurnTimeout)
	}
	for _, p := range cfg.dshProfiles {
		if p.Model.ModelID != saved.Runtime.Model.ModelID || p.Model.Compat.SupportsDeveloperRole == nil || *p.Model.Compat.SupportsDeveloperRole {
			t.Fatal("global change rewrote existing Room runtime")
		}
	}
	newRoom := createTeamRoom(t, m, "new-runtime")
	fresh, _ := readManagedRoom(t, m, newRoom)
	if fresh.Runtime.Model.ModelID != defaults.Model.ModelID || fresh.Runtime.Model.Compat.SupportsDeveloperRole == nil || !*fresh.Runtime.Model.Compat.SupportsDeveloperRole {
		t.Fatal("new Room missed updated default")
	}
	team.Runtime = testTeamDefinition(t).Runtime
	team.Runtime.Model.ModelID = "team-specific-model"
	writeTestTeam(t, teams, team)
	loaded, err := loadTeams(teams, defaults)
	if err != nil || loaded[team.ID].Runtime.Model.ModelID != "team-specific-model" {
		t.Fatal("complete team runtime did not replace default", err)
	}
	// A partial override must not silently inherit omitted fields.
	team.Runtime.DSH.BinaryPath = ""
	writeTestTeam(t, teams, team)
	if _, err := loadTeams(teams, defaults); err == nil {
		t.Fatal("partial runtime override merged with default")
	}
}
