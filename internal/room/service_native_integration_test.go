//go:build integration && darwin

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Opt-in real launchd acceptance uses a unique test label, isolated account
// HOME and data. It never registers the user's production com.teamx.server.
func TestServiceNativeLaunchAgentArchive(t *testing.T) {
	archive := os.Getenv("TEAMX_TEST_SERVICE_ARCHIVE")
	if archive == "" {
		t.Skip("set TEAMX_TEST_SERVICE_ARCHIVE to a native standard archive")
	}
	output := os.Getenv("TEAMX_TEST_SERVICE_OUTPUT")
	if output == "" {
		t.Fatal("set TEAMX_TEST_SERVICE_OUTPUT to a new private evidence directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	output, _ = filepath.EvalSymlinks(output)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := serviceHash(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r := serviceRegistration{Version: 1, Root: filepath.Join(output, "server"), Home: filepath.Join(output, "home"), UserHome: filepath.Join(output, "account"), UID: os.Getuid(), GID: os.Getgid(), Platform: "darwin", User: "teamx-native-test", LogRoot: filepath.Join(output, "logs")}
	r.Unit = filepath.Join(r.UserHome, "Library/LaunchAgents/com.teamx.server.plist")
	for _, dir := range []string{r.Root, r.Home, r.LogRoot, filepath.Dir(r.Unit)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	release, err := publishServiceArchive(r.Root, archive, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := selectServiceRelease(r.Root, release); err != nil {
		t.Fatal(err)
	}
	if err := registerServerInstallation(serverLayout{Root: r.Root, Release: release, Home: r.Home}); err != nil {
		t.Fatal(err)
	}
	var installation serverInstallation
	if err := readSnapshot(filepath.Join(r.Root, "installation.json"), &installation, 16<<10); err != nil {
		t.Fatal(err)
	}
	r.InstallationID = installation.InstanceID
	if err := writeServiceJSON(filepath.Join(r.Root, "service.json"), r); err != nil {
		t.Fatal(err)
	}
	if err := initializeServiceHome(ctx, r, release); err != nil {
		t.Fatal(err)
	}
	config, err := readStartupConfig(filepath.Join(r.Home, "config.toml"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	// Choose a currently free test port. It is fixed in config before launch.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	config.Server.Listen = &address
	data, err := toml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	initialToken, err := os.ReadFile(filepath.Join(r.Home, "auth/admin-token"))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := serviceTemplate(r)
	if err != nil {
		t.Fatal(err)
	}
	label := fmt.Sprintf("com.teamx.server.test.%d.%d", os.Getpid(), time.Now().UnixNano())
	unit = []byte(strings.ReplaceAll(string(unit), "<string>com.teamx.server</string>", "<string>"+label+"</string>"))
	if err := os.WriteFile(r.Unit, unit, 0644); err != nil {
		t.Fatal(err)
	}
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		for i, arg := range args {
			if strings.HasSuffix(arg, "/com.teamx.server") {
				args[i] = strings.TrimSuffix(arg, "com.teamx.server") + label
			}
		}
		return runServiceCommand(ctx, name, args...)
	}
	manager := func(action string) error { return serviceManager(ctx, runner, r, action) }
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		defer cancel()
		_ = serviceManager(cleanupCtx, runner, r, "stop")
	})
	checks := []string{}
	if err := manager("start"); err != nil {
		t.Fatal(err)
	}
	status, err := waitServiceStatus(ctx, r)
	if err != nil || status["status"] != "setup_required" {
		t.Fatal(status, err)
	}
	checks = append(checks, "real launchd starts archive without model Key exports; authenticated setup_required")
	if err := manager("stop"); err != nil {
		t.Fatal(err)
	}
	unlock, err := acquireStoppedService(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(output, "backup")
	if err := createServiceBackup(r, backup); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	time.Sleep(1500 * time.Millisecond)
	if _, err := authenticatedServiceStatus(ctx, r); err == nil {
		t.Fatal("explicit stop was undone")
	}
	checks = append(checks, "bootout settles home/data and remains stopped; consistent archive backup verified")
	serviceTestFile(t, filepath.Join(r.Home, "model-operations/interrupted.json"), `{"committed":true,"status":"interrupted"}`, 0600)
	unlock, err = acquireStoppedService(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreServiceBackup(ctx, r, backup); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	if err := manager("start"); err != nil {
		t.Fatal(err)
	}
	status, err = waitServiceStatus(ctx, r)
	if err != nil || status["status"] != "setup_required" {
		t.Fatal(status, err)
	}
	token, _ := os.ReadFile(filepath.Join(r.Home, "auth/admin-token"))
	if string(token) != string(initialToken) {
		t.Fatal("restore changed operator token")
	}
	checks = append(checks, "same-path backup restore restarts original archive and preserves authentication")
	if err := manager("stop"); err != nil {
		t.Fatal(err)
	}
	unlock, err = acquireStoppedService(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := verifyServiceRelease(release); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "passed", "archive_sha256": digest, "label": label, "checks": checks, "real_platforms": false, "host_reboot": false, "scope": "isolated native macOS launchd setup/stop/backup/restore; no real model or platform"}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "report.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
