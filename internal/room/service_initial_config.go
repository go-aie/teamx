//go:build darwin || linux

package room

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/pelletier/go-toml/v2"
)

// A deployment seed selects only host settings before first-access reserves
// an identity. Runtime paths, credentials and business state have other owners.
func readServiceInitialConfig(path string) (map[string]any, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 {
		return nil, errors.New("deployment config requires a private 0600 regular TOML file (maximum 64 KiB)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c startupConfig
	var document map[string]any
	if toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&c) != nil || toml.Unmarshal(data, &document) != nil {
		return nil, errors.New("invalid initial deployment configuration")
	}
	for key := range document {
		if key != "server" && key != "deployment" {
			return nil, errors.New("initial deployment config accepts only server and deployment settings")
		}
	}
	server, _ := document["server"].(map[string]any)
	for key := range server {
		switch key {
		case "listen", "access_mode", "public_base_url", "trusted_proxies", "time_zone":
		default:
			return nil, errors.New("initial deployment config cannot choose data roots or business defaults")
		}
	}
	deployment, _ := document["deployment"].(map[string]any)
	if _, ok := deployment["execution"]; ok {
		return nil, errors.New("use --execution and --srt-endpoint to select installed execution")
	}
	if c.Deployment.Mode != "personal" && c.Deployment.Mode != "multi-user" {
		return nil, errors.New("initial deployment config requires deployment.mode personal or multi-user")
	}
	if c.Deployment.Mode == "multi-user" {
		if _, err := c.Deployment.Authentication.provider(); err != nil {
			return nil, err
		}
	} else if c.Deployment.Authentication.Provider != "" || c.Deployment.GitLab.Enabled {
		return nil, errors.New("managed authentication and GitLab require multi-user deployment")
	}
	if err := c.Deployment.Resources.validate(); err != nil {
		return nil, err
	}
	if c.Server.Listen == nil {
		c.Server.Listen = new("127.0.0.1:8088")
	}
	if err := validateManagedListen(*c.Server.Listen); err != nil {
		return nil, err
	}
	if err := validateRemoteAccess(c, true); err != nil {
		return nil, err
	}
	return document, nil
}

func initializeServiceConfig(r serviceRegistration, release string, initial map[string]any) error {
	deployment := initial["deployment"].(map[string]any)
	if deployment["mode"] == "multi-user" && r.Execution != "srt" {
		return errors.New("multi-user initial configuration requires --execution srt")
	}
	path := filepath.Join(r.Home, "config.toml")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errors.New("existing service configuration must be a private regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var current map[string]any
		if toml.Unmarshal(data, &current) != nil {
			return errors.New("existing service configuration is invalid; retained for repair")
		}
		for section, value := range initial {
			actual, _ := current[section].(map[string]any)
			for key, expected := range value.(map[string]any) {
				if !reflect.DeepEqual(actual[key], expected) {
					return errors.New("initial deployment configuration differs from existing home; refusing to rebind it")
				}
			}
		}
		return nil // Interrupted installation resumes without changing settings.
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Missing config beside old identity evidence is damage, not first install.
	for _, name := range []string{"first-access.json", "personal-space-registration.json", "multi-user-registration.json", "users.json", "spaces.json", "auth", "data"} {
		if _, err := os.Lstat(filepath.Join(r.Home, name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("initial deployment configuration requires a fresh home")
		}
	}
	document := bundleStartupConfig(release, r.Home, filepath.Join(r.Root, "current/bin/dsh"))
	for section, value := range initial {
		if section == "server" {
			for key, entry := range value.(map[string]any) {
				document["server"].(map[string]any)[key] = entry
			}
		} else {
			document[section] = value
		}
	}
	data, err := toml.Marshal(document)
	if err != nil {
		return err
	}
	if err := installMissing(path, data); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return os.Lchown(path, r.UID, r.GID)
	}
	return nil
}
