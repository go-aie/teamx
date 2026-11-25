//go:build darwin || linux

package room

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/go-aie/teamx/internal/agent/srt"
	"github.com/pelletier/go-toml/v2"
)

func serviceExecutionConfig(r serviceRegistration, release string) (srt.Config, error) {
	if r.Execution != "srt" || r.Platform != "linux" || runtime.GOARCH != "amd64" || r.UID == 0 {
		return srt.Config{}, errors.New("SRT service requires Linux amd64 and an ordinary service account")
	}
	return srt.InstalledConfig(release, []string{r.Home}, r.SRTEndpoints)
}

// Called only by explicit installation or a stopped, backed-up upgrade. The
// descriptor records the physical release, never the moving current symlink.
// Old descriptors and release trees remain available to backups and recovery.
func configureServiceExecution(r serviceRegistration, release string) error {
	if r.Execution == "" || r.Execution == "native" {
		return nil
	}
	config, err := serviceExecutionConfig(r, release)
	if err != nil {
		return err
	}
	return writeServiceExecutionConfig(r, release, config)
}

func writeServiceExecutionConfig(r serviceRegistration, release string, config srt.Config) error {
	path := filepath.Join(r.Home, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return err
	}
	deployment, ok := document["deployment"].(map[string]any)
	if !ok {
		deployment = map[string]any{}
		document["deployment"] = deployment
	}
	if execution, ok := deployment["execution"].(map[string]any); ok {
		if kind, _ := execution["kind"].(string); kind != "" && kind != "native" && kind != "srt" {
			return errors.New("service execution configuration conflicts with registration")
		}
	}
	dir := filepath.Join(r.Home, "execution")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("SRT descriptor directory must be a private physical directory")
	}
	manifest := filepath.Join(dir, filepath.Base(release)+".json")
	if err := writeServiceJSON(manifest, config); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, r.UID, r.GID); err != nil {
			return err
		}
		if err := os.Chown(manifest, r.UID, r.GID); err != nil {
			return err
		}
	}
	deployment["execution"] = map[string]any{"kind": "srt", "manifest": manifest}
	data, err = toml.Marshal(document)
	if err != nil {
		return err
	}
	if err := writeServiceFile(path, data, 0600); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return os.Chown(path, r.UID, r.GID)
	}
	return nil
}

func validateServiceExecutionSelection(kind string, endpoints []string) error {
	if kind != "" && kind != "native" && kind != "srt" {
		return errors.New("--execution must be native or srt")
	}
	if kind != "srt" {
		if len(endpoints) != 0 {
			return errors.New("--srt-endpoint requires --execution srt")
		}
		return nil
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("SRT installation requires Linux amd64 standard")
	}
	if len(endpoints) == 0 {
		return errors.New("SRT installation requires repeated --srt-endpoint origins including the fixed internal callback and model upstreams")
	}
	return nil
}
