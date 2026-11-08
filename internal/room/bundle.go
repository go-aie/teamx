package room

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Bundle commands never read the developer's default configuration.
func runBundle(command string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("bundle-root", "", "distribution directory")
	home := flags.String("home", "", "private configuration, teams and data directory")
	config := flags.String("config", "", "configuration to diagnose")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return ErrRequest
	}
	if *root == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		*root = filepath.Dir(filepath.Dir(executable))
	}
	var err error
	*root, err = filepath.Abs(*root)
	if err != nil {
		return err
	}
	if *home == "" {
		*home = filepath.Join(*root, ".teamx")
	}
	*home, err = filepath.Abs(*home)
	if err != nil {
		return err
	}
	if command != "doctor" && *config != "" {
		return fmt.Errorf("--config is only supported by doctor; use --home for init/start")
	}
	if *config == "" {
		*config = filepath.Join(*home, "config.toml")
	}
	*config, err = filepath.Abs(*config)
	if err != nil {
		return err
	}
	if command == "doctor" {
		return bundleDoctor(*root, *config, stdout)
	}
	if err = os.MkdirAll(*home, 0700); err != nil {
		return err
	}
	lock, err := lockBundleHome(*home)
	if err != nil {
		return err
	}
	defer unlockDirectory(lock)
	if err = initializeBundle(*root, *home); err != nil {
		return err
	}
	if command == "init" {
		_, err = fmt.Fprintf(stdout, "Configuration: %s\nSet runtime.model.base_url, runtime.model.model_id and the credential environment before starting.\n", *config)
		return err
	}
	if err = bundleDoctor(*root, *config, stdout); err != nil {
		return err
	}
	return serve([]string{"--config", *config}, stdout, stderr)
}

func initializeBundle(root, home string) error {
	// TOML is serialized by Go, including arbitrary spaces, quotes and backslashes.
	config := map[string]any{
		"server":  map[string]any{"listen": "127.0.0.1:8088", "data_dir": "./data", "teams_dir": "./teams", "max_turns": 16, "dsh_turn_timeout": "15m"},
		"runtime": map[string]any{"dsh": map[string]any{"binary_path": filepath.Join(root, "bin", "dsh")}, "model": map[string]any{"protocol": "openai-completions", "base_url": "", "model_id": "", "api_key_env": "OPENAI_API_KEY"}},
	}
	// Portable homes use a config-relative path; explicit external homes bind this bundle.
	if home == filepath.Join(root, ".teamx") {
		config["runtime"].(map[string]any)["dsh"].(map[string]any)["binary_path"] = "../bin/dsh"
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return err
	}
	if err = installMissing(filepath.Join(home, "config.toml"), data); err != nil {
		return err
	}
	source := filepath.Join(root, "share", "teamx", "teams")
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(home, "teams", rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("template symlink rejected: %s", rel)
		}
		if entry.IsDir() {
			if info, err := os.Lstat(dest); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("template destination is a symlink: %s", dest)
			}
			return os.MkdirAll(dest, 0700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("invalid template: %s", rel)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return installMissing(dest, b)
	})
}

// A temporary complete file is published with link(2), which cannot replace an
// existing user file. A crash leaves either the previous file or a complete new one.
func installMissing(path string, data []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".init-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), path); errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

type doctorCheck struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Required bool   `json:"required"`
}

func bundleDoctor(root, config string, out io.Writer) error {
	checks := []doctorCheck{}
	failed := false
	add := func(name, path, status, detail string, required bool) {
		checks = append(checks, doctorCheck{name, path, status, detail, required})
		if required && status != "ok" {
			failed = true
		}
	}
	raw, err := os.ReadFile(config)
	if err != nil {
		return fmt.Errorf("configuration %s: %w; run start init", config, err)
	}
	var c startupConfig
	if err = toml.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("invalid configuration %s; check TOML syntax", config)
	}
	missing := []string{}
	if c.Runtime == nil {
		missing = append(missing, "runtime")
	} else {
		m := c.Runtime.Model
		if m.Protocol == "" {
			missing = append(missing, "runtime.model.protocol")
		}
		if (m.BaseURL == "" && m.Protocol != "deepseek-messages") || strings.Contains(m.BaseURL, "model.example") {
			missing = append(missing, "runtime.model.base_url")
		}
		if m.ModelID == "" || m.ModelID == "your-model" {
			missing = append(missing, "runtime.model.model_id")
		}
		key, exported := os.LookupEnv(m.APIKeyEnv)
		if !exported {
			key = c.Env[m.APIKeyEnv]
		}
		if m.APIKeyEnv == "" || key == "" {
			missing = append(missing, "credential environment "+m.APIKeyEnv)
		}
	}
	status := "ok"
	if len(missing) > 0 {
		status = "missing"
	}
	add("configuration", config, status, strings.Join(missing, ", "), true)
	if len(missing) == 0 {
		loaded, e := loadStartupConfig(config, true)
		if e != nil {
			add("configuration validation", config, "invalid", e.Error(), true)
		} else {
			teams, e := loadTeams(*loaded.Server.TeamsDir, loaded.Runtime)
			if e != nil {
				add("teams", *loaded.Server.TeamsDir, "invalid", e.Error(), true)
			} else if len(teams) == 0 {
				add("teams", *loaded.Server.TeamsDir, "missing", "no teams found", true)
			} else {
				add("teams", *loaded.Server.TeamsDir, "ok", fmt.Sprintf("%d teams", len(teams)), true)
			}
		}
	}
	var manifest struct {
		Profile      string `json:"profile"`
		Dependencies map[string]struct {
			Version               string `json:"version"`
			ExpectedVersion       string `json:"expected_version"`
			ExpectedVersionPrefix string `json:"expected_version_prefix"`
		} `json:"dependencies"`
	}
	b, e := os.ReadFile(filepath.Join(root, "manifest.json"))
	if e == nil {
		e = json.Unmarshal(b, &manifest)
	}
	if e == nil && (manifest.Profile != "core" && manifest.Profile != "full" || manifest.Dependencies["node"].ExpectedVersion == "" || manifest.Dependencies["dsh"].ExpectedVersion == "") {
		e = errors.New("incomplete manifest")
	}
	if e != nil {
		add("manifest", filepath.Join(root, "manifest.json"), "missing", "bundle manifest required", true)
	}
	commands := []struct {
		name, path string
		args       []string
		required   bool
	}{
		{"node", filepath.Join(root, "runtime/node/bin/node"), []string{"--version"}, true},
		{"dsh", filepath.Join(root, "bin/dsh"), []string{"--version"}, true},
		{"glab", filepath.Join(root, "bin/glab"), []string{"version"}, manifest.Profile == "full"},
		{"lark-cli", filepath.Join(root, "bin/lark-cli"), []string{"--version"}, manifest.Profile == "full"},
		{"git", filepath.Join(root, "bin/git"), []string{"--version"}, manifest.Profile == "full"},
	}
	if c.Runtime != nil && c.Runtime.DSH.BinaryPath != "" {
		commands[1].path = configPath(filepath.Dir(config), c.Runtime.DSH.BinaryPath)
	}
	for _, tool := range commands {
		path := tool.path
		if !tool.required {
			if _, e := os.Stat(path); e != nil {
				if p, e := exec.LookPath(tool.name); e == nil {
					path = p
				}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, path, tool.args...)
		cmd.WaitDelay = time.Second
		b, e := cmd.Output()
		cancel()
		status, detail := "ok", strings.TrimSpace(string(b))
		if e != nil {
			status = "missing"
			detail = "unavailable; optional unless this capability is used"
			if tool.required {
				detail = "required runtime unavailable"
			}
		} else {
			dep := manifest.Dependencies[tool.name]
			if dep.ExpectedVersion != "" && detail != dep.ExpectedVersion || dep.ExpectedVersionPrefix != "" && !strings.HasPrefix(detail, dep.ExpectedVersionPrefix+" ") && detail != dep.ExpectedVersionPrefix {
				status = "mismatch"
				detail = "expected " + dep.Version
			}
		}
		add(tool.name, path, status, detail, tool.required)
	}
	add("scion", "", "deferred", "outside current distribution scope", false)
	if err = json.NewEncoder(out).Encode(struct {
		Checks []doctorCheck `json:"checks"`
		Ready  bool          `json:"ready"`
	}{checks, !failed}); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("bundle not ready; configuration: %s; resolve required checks above", config)
	}
	return nil
}

// start delegates argument handling to Go and holds the home lock through shutdown.
func bundleEntry(args []string, stdout, stderr io.Writer) error {
	command := "launch"
	filtered := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "init" || arg == "doctor" {
			if command != "launch" {
				return ErrRequest
			}
			command = arg
			continue
		}
		filtered = append(filtered, arg)
		if arg == "--bundle-root" || arg == "--home" || arg == "--config" {
			if i+1 >= len(args) {
				return ErrRequest
			}
			i++
			filtered = append(filtered, args[i])
		}
	}

	return runBundle(command, filtered, stdout, stderr)
}

// Populated by the distribution build; ordinary source builds remain identifiable.
var distributionVersion = "dev"
var distributionCommit = "unknown"
var distributionDirty = "unknown"
