package room

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Startup settings stay outside Room snapshots. Only the resolved runtime is frozen.
type startupConfig struct {
	Server struct {
		Listen         *string             `toml:"listen"`
		DataDir        *string             `toml:"data_dir"`
		TeamsDir       *string             `toml:"teams_dir"`
		MaxTurns       *uint64             `toml:"max_turns"`
		MergeWindow    *string             `toml:"merge_window"`
		DSHTurnTimeout *string             `toml:"dsh_turn_timeout"`
		ImageMaterials imageMaterialConfig `toml:"image_materials"`
	} `toml:"server"`
	Runtime *teamRuntime      `toml:"runtime"`
	Env     map[string]string `toml:"env"`
}

var configEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func defaultStartupConfigPath() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".teamx", "config.toml"), nil
}

func loadStartupConfig(name string, required bool) (startupConfig, error) {
	var config startupConfig
	path, err := filepath.Abs(name)
	if err != nil {
		return config, err
	}
	f, err := os.Open(path)
	if err != nil && !(errors.Is(err, os.ErrNotExist) && !required) {
		return config, err
	}
	if err == nil {
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return config, fmt.Errorf("cannot read config %q (maximum 1 MiB)", path)
		}
		if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&config); err != nil {
			// Decoder errors can contain source values, including credentials.
			var decode *toml.DecodeError
			if errors.As(err, &decode) {
				line, column := decode.Position()
				return startupConfig{}, fmt.Errorf("invalid config %q at line %d, column %d; check TOML syntax and field types", path, line, column)
			}
			return startupConfig{}, fmt.Errorf("invalid config %q; check TOML syntax and fields against config.example.toml", path)
		}
	}
	if _, err := config.Server.ImageMaterials.limits(); err != nil {
		return startupConfig{}, err
	}
	base := filepath.Dir(path)
	if config.Server.DataDir == nil {
		value := "./data"
		config.Server.DataDir = &value
	}
	if config.Server.TeamsDir == nil {
		value := "./teams"
		config.Server.TeamsDir = &value
	}
	for _, dir := range []*string{config.Server.DataDir, config.Server.TeamsDir} {
		if dir != nil && *dir != "" {
			*dir = configPath(base, *dir)
		}
	}
	if config.Runtime != nil {
		r := config.Runtime
		if r.DSH.BinaryPath != "" {
			r.DSH.BinaryPath = configPath(base, r.DSH.BinaryPath)
		}
		for i, arg := range r.DSH.BinaryArgs {
			if strings.HasSuffix(arg, ".js") || strings.HasSuffix(arg, ".mjs") {
				r.DSH.BinaryArgs[i] = configPath(base, arg)
			}
		}
		if err := validateTeamRuntime(*r); err != nil {
			return startupConfig{}, fmt.Errorf("config runtime: %w", err)
		}
	}
	for key, value := range config.Env {
		if !configEnvName.MatchString(key) || isRoomToolEnv(key) || key == "TEAMX_CLI" || strings.ContainsRune(value, 0) {
			return startupConfig{}, errors.New("invalid config env entry; use valid variable names and do not set TeamX member identity")
		}
		if (key == "TEAMX_GITLAB_BIN" || key == "TEAMX_GLAB_BIN") && value != "" {
			config.Env[key] = configPath(base, value)
		}
	}
	return config, nil
}

func configPath(base, name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(base, name)
}

// The service loads env once before starting workers; caller restores it after shutdown.
// An explicitly exported value, including an empty value, always wins.
func applyStartupEnv(values map[string]string) (func(), error) {
	var added []string
	cleanup := func() {
		for _, key := range added {
			_ = os.Unsetenv(key)
		}
	}
	for key, value := range values {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			cleanup()
			return nil, errors.New("cannot apply config environment")
		}
		added = append(added, key)
	}
	return cleanup, nil
}

func applyStartupSettings(flags *flag.FlagSet, explicit map[string]bool, startup startupConfig) error {
	settings := map[string]*string{"listen": startup.Server.Listen, "merge-window": startup.Server.MergeWindow, "dsh-turn-timeout": startup.Server.DSHTurnTimeout}
	if startup.Server.MaxTurns != nil {
		value := strconv.FormatUint(*startup.Server.MaxTurns, 10)
		settings["max-turns"] = &value
	}
	for name, value := range settings {
		if value != nil && !explicit[name] {
			if err := flags.Set(name, *value); err != nil {
				return fmt.Errorf("%w: invalid server.%s in config", ErrRequest, name)
			}
		}
	}
	return nil
}
