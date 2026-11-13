//go:build darwin || linux

package room

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestInstalledServiceCommandFollowsCurrentAndCustomRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "server with spaces")
	command := filepath.Join(t.TempDir(), "teamx")
	serviceTestFile(t, filepath.Join(root, "service.json"), `{}`, 0600)
	for _, version := range []string{"first", "second"} {
		serviceTestFile(t, filepath.Join(root, "releases", version, "bin", "teamx"), "fixture", 0755)
	}
	current := filepath.Join(root, "current")
	if err := os.Symlink("releases/first", current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(current, "bin", "teamx"), command); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := installedServiceRoot(command); got != want {
		t.Fatalf("installed root = %q, want %q", got, want)
	}
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("releases/second", current); err != nil {
		t.Fatal(err)
	}
	if got := installedServiceRoot(command); got != want {
		t.Fatalf("upgraded root = %q, want %q", got, want)
	}
	portable := filepath.Join(t.TempDir(), "bin", "teamx")
	serviceTestFile(t, portable, "fixture", 0755)
	if got := installedServiceRoot(portable); got != "" {
		t.Fatalf("portable command inferred an installation: %q", got)
	}
}

func TestServiceHelpAndInstallResultGuideBrowserSetup(t *testing.T) {
	for _, args := range [][]string{{"service"}, {"service", "--help"}, {"service", "stop", "--help"}} {
		var stdout, stderr bytes.Buffer
		if err := Run(args, &stdout, &stderr); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if stdout.Len()+stderr.Len() == 0 {
			t.Fatalf("%v returned no help", args)
		}
	}
	home := t.TempDir()
	serviceTestFile(t, filepath.Join(home, "config.toml"), "[server]\nlisten = '0.0.0.0:8091'\naccess_mode = 'lan'\n[access]\nlogin_enabled = true\nadmin_token_file = 'auth/custom-token'\n", 0600)
	var stdout, stderr bytes.Buffer
	if err := writeServiceInstallResult(serviceRegistration{Home: home, Platform: runtime.GOOS}, map[string]any{"authenticated": true}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"TeamX 已安装并启动。", "1. 打开：http://127.0.0.1:8091", filepath.Join(home, "auth/custom-token"), "管理令牌填入网页登录框", "3. 登录后按页面提示配置模型", "cat '"} {
		if !strings.Contains(stderr.String(), text) {
			t.Fatalf("missing %q in install result: %s", text, stderr.String())
		}
	}
	var c startupConfig
	c.Server.AccessMode = accessProxy
	c.Server.PublicBaseURL = "https://teamx.example"
	if got := serviceBrowserURL(c); got != c.Server.PublicBaseURL {
		t.Fatalf("browser URL = %q", got)
	}
}

func serviceTestFile(t *testing.T, name, value string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

func serviceTestRelease(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"start", "bin/teamx", "bin/dsh", "bin/glab", "bin/lark-cli", "runtime/node/bin/node"} {
		serviceTestFile(t, filepath.Join(root, name), "fixture-"+version, 0755)
	}
	serviceTestFile(t, filepath.Join(root, "LICENSE"), "license", 0644)
	serviceTestFile(t, filepath.Join(root, "runtime/example/child.js"), "child", 0644)
	serviceTestFile(t, filepath.Join(root, "runtime/example.js"), "sibling", 0644)
	serviceTestFile(t, filepath.Join(root, "THIRD_PARTY_NOTICES/notice"), "notice", 0644)
	serviceTestFile(t, filepath.Join(root, "share/teamx/teams/personal-assistant/team.json"), `{}`, 0644)
	if err := os.Symlink("node", filepath.Join(root, "runtime/node/bin/node-link")); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"schema_version": 1, "version": version, "profile": "standard", "target": runtime.GOOS + "-" + runtime.GOARCH,
		"channel": "dev", "source": map[string]any{"commit": "fixture"}, "dependencies": map[string]any{}, "acceptance": map[string]any{}}
	if err := writeServiceJSON(filepath.Join(root, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	serviceTestChecksums(t, root)
	return root
}

func serviceTestChecksums(t *testing.T, root string) {
	t.Helper()
	items, err := serviceInventory(root)
	if err != nil {
		t.Fatal(err)
	}
	filtered := []serviceAsset{}
	for _, item := range items {
		if item.Path != "checksums.txt" {
			filtered = append(filtered, item)
		}
	}
	// Match the Node packager's sorted directory traversal, not a global path
	// sort: example/child.js appears before example.js.
	sort.Slice(filtered, func(i, j int) bool {
		return strings.ReplaceAll(filtered[i].Path, "/", "\x00") < strings.ReplaceAll(filtered[j].Path, "/", "\x00")
	})
	if err := writeServiceJSON(filepath.Join(root, "checksums.txt"), filtered); err != nil {
		t.Fatal(err)
	}
}

func serviceTestArchive(t *testing.T, root string, extra ...*tar.Header) (string, string) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "standard.tar.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, name)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(name)
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = "bundle"
		if rel != "." {
			h.Name += "/" + filepath.ToSlash(rel)
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			_, err = tw.Write(b)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range extra {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(tw.Close(), gz.Close(), f.Close()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := serviceHash(bytes.NewReader(b))
	return name, digest
}

func TestServiceArchivePublicationAndValidation(t *testing.T) {
	root := t.TempDir()
	archive, digest := serviceTestArchive(t, serviceTestRelease(t, "dev-test"))
	first, err := publishServiceArchive(root, archive, digest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := publishServiceArchive(root, archive, digest)
	if err != nil || first != second {
		t.Fatal(second, err)
	}
	if err := selectServiceRelease(root, first); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(first, digest[:12]) {
		t.Fatal(first)
	}
	if _, err := publishServiceArchive(root, archive, strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted wrong digest")
	}
	serviceTestFile(t, filepath.Join(first, "bin/dsh"), "tampered", 0755)
	if _, err := publishServiceArchive(root, archive, digest); err == nil {
		t.Fatal("accepted changed installed content")
	}
	for _, h := range []*tar.Header{
		{Name: "bundle/../escape", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: "/absolute", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: "bundle/bin/teamx", Typeflag: tar.TypeReg, Mode: 0755},
		{Name: "another/entry", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: "bundle/link", Linkname: "../../escape", Typeflag: tar.TypeSymlink, Mode: 0777},
		{Name: "bundle/link", Linkname: "bin/teamx", Typeflag: tar.TypeLink, Mode: 0755},
	} {
		t.Run(h.Name+string(h.Typeflag), func(t *testing.T) {
			a, hash := serviceTestArchive(t, serviceTestRelease(t, "dev-bad"), h)
			if _, err := publishServiceArchive(t.TempDir(), a, hash); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
	for _, name := range []string{".teamx/config.toml", "model-credentials/key", "auth/admin-token", "bin/extra"} {
		t.Run(name, func(t *testing.T) {
			release := serviceTestRelease(t, "dev-private")
			serviceTestFile(t, filepath.Join(release, name), "private", 0600)
			if name != "bin/extra" {
				serviceTestChecksums(t, release)
			}
			a, hash := serviceTestArchive(t, release)
			if _, err := publishServiceArchive(t.TempDir(), a, hash); err == nil {
				t.Fatal("accepted private or unlisted content")
			}
		})
	}
}

func serviceTestRegistration(t *testing.T) serviceRegistration {
	t.Helper()
	base := t.TempDir()
	base, _ = filepath.EvalSymlinks(base)
	r := serviceRegistration{Version: 1, Root: filepath.Join(base, "server"), Home: filepath.Join(base, "home"), UID: os.Getuid(), GID: os.Getgid(), InstallationID: "test-instance", Platform: runtime.GOOS}
	if err := os.MkdirAll(r.Root, 0755); err != nil {
		t.Fatal(err)
	}
	a, hash := serviceTestArchive(t, serviceTestRelease(t, "dev-old"))
	release, err := publishServiceArchive(r.Root, a, hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := selectServiceRelease(r.Root, release); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.Home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := initializeBundleRuntime(release, r.Home, filepath.Join(r.Root, "current/bin/dsh")); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestServiceConsistentBackupRestorePreservesPrivateStateAndVersions(t *testing.T) {
	r := serviceTestRegistration(t)
	files := map[string]string{
		"auth/admin-token":                              "synthetic-admin-token",
		"model-credentials/historical-key":              "synthetic-key",
		"model-operations/committed.json":               `{"status":"interrupted","committed":true,"completed":["member-a"]}`,
		"model-migration.json":                          `{"status":"completed"}`,
		"model-migration-backup/config.toml":            "legacy",
		"model-validations/receipt.json":                `{"validated":true}`,
		"first-access.json":                             `{"status":"completed"}`,
		"data/room/session/rotation.json":               `{"completed":true,"id":"rotation-original"}`,
		"data/room/workspace/file":                      "workspace",
		"teams/personal-assistant/prompts/assistant.md": "user template edit",
	}
	for name, value := range files {
		serviceTestFile(t, filepath.Join(r.Home, name), value, 0600)
	}
	releaseLock, err := acquireStoppedService(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(r.Root), "private-backup")
	if err := createServiceBackup(r, backup); err != nil {
		t.Fatal(err)
	}
	releaseLock()
	original, _ := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	a, hash := serviceTestArchive(t, serviceTestRelease(t, "dev-new"))
	newRelease, err := publishServiceArchive(r.Root, a, hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := selectServiceRelease(r.Root, newRelease); err != nil {
		t.Fatal(err)
	}
	for name := range files {
		serviceTestFile(t, filepath.Join(r.Home, name), "changed", 0600)
	}
	releaseLock, err = acquireStoppedService(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreServiceBackup(context.Background(), r, backup); err != nil {
		t.Fatal(err)
	}
	releaseLock()
	for name, value := range files {
		b, err := os.ReadFile(filepath.Join(r.Home, name))
		if err != nil || string(b) != value {
			t.Fatal("restore changed private state", name, err)
		}
	}
	selected, _ := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	if selected != original {
		t.Fatal(selected, original)
	}
	if _, err := os.Stat(newRelease); err != nil {
		t.Fatal("deleted later runtime", err)
	}
	if err := createServiceBackup(r, backup); err == nil {
		t.Fatal("overwrote backup")
	}
	wrong := r
	wrong.InstallationID = "different"
	if _, err := verifyServiceBackup(backup, wrong); err == nil {
		t.Fatal("accepted other installation")
	}
	serviceTestFile(t, filepath.Join(backup, "home/model-credentials/historical-key"), "corrupt", 0600)
	if _, err := verifyServiceBackup(backup, r); err == nil {
		t.Fatal("accepted corrupt backup")
	}
}

func TestServiceStoppedLocksAndExternalBackupGate(t *testing.T) {
	r := serviceTestRegistration(t)
	lock, err := lockBundleHome(r.Home)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := acquireStoppedService(ctx, r); err == nil {
		t.Fatal("backed up running home")
	}
	_ = unlockDirectory(lock)
	if err := os.MkdirAll(filepath.Join(r.Home, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err = lockDirectory(filepath.Join(r.Home, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireStoppedService(context.Background(), r); !errors.Is(err, ErrLocked) {
		t.Fatal("ignored old data writer", err)
	}
	_ = unlockDirectory(lock)
	if err := os.Symlink(t.TempDir(), filepath.Join(r.Home, "external")); err != nil {
		t.Fatal(err)
	}
	if err := createServiceBackup(r, filepath.Join(filepath.Dir(r.Root), "backup")); err == nil {
		t.Fatal("silently omitted external state")
	}
	for _, dest := range []string{filepath.Join(r.Home, "backup"), filepath.Join(r.Root, "backup")} {
		if err := createServiceBackup(r, dest); err == nil {
			t.Fatal("accepted nested backup")
		}
	}
	installLock, err := lockDirectoryFile(r.Root, ".service.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlockDirectory(installLock)
	if err := runService([]string{"inspect", "--server-root", r.Root}, io.Discard, io.Discard); !errors.Is(err, ErrLocked) {
		t.Fatal("concurrent deployment accepted", err)
	}
}

func TestServiceTemplatesAndManagerStopSemantics(t *testing.T) {
	r := serviceRegistration{Platform: "linux", Root: "/opt/Team X $%", Home: "/var/lib/teamx", User: "teamx", UserHome: "/home/teamx", UID: 501, GID: 20, InstallationID: "test-instance", Unit: filepath.Join(t.TempDir(), "unit")}
	b, err := serviceTemplate(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"KillMode=mixed", "TimeoutStopSec=45", "StartLimitBurst=5", "RestartSec=10", "User=501", "WorkingDirectory=/var/lib/teamx", "Team X $$%%"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatal(want, string(b))
		}
	}
	serviceTestFile(t, r.Unit, string(b), 0644)
	if err := checkServiceUnit(r); err != nil {
		t.Fatal(err)
	}
	serviceTestFile(t, r.Unit, "unknown unit", 0644)
	if err := checkServiceUnit(r); err == nil {
		t.Fatal("controlled unknown service")
	}
	var calls [][]string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte("state = running"), nil
	}
	if err := serviceManager(context.Background(), runner, r, "stop"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"/usr/bin/systemctl", "stop", "teamx.service"}}) {
		t.Fatal(calls)
	}
	r.Platform, r.LogRoot = "darwin", filepath.Join(t.TempDir(), "logs")
	r.Root = "/Users/test/中文 & <TeamX>"
	b, err = serviceTemplate(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("&amp; &lt;TeamX&gt;")) || !bytes.Contains(b, []byte("<key>KeepAlive</key><false/>")) {
		t.Fatal(string(b))
	}
	if runtime.GOOS == "darwin" {
		name := filepath.Join(t.TempDir(), "service.plist")
		serviceTestFile(t, name, string(b), 0644)
		if out, err := exec.Command("/usr/bin/plutil", "-lint", name).CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	calls = nil
	if err := serviceManager(context.Background(), runner, r, "stop"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1][1] != "bootout" {
		t.Fatal(calls)
	}
	// No enable/disable or kill-by-PID is used to implement stop.
}

func TestServiceLogRotation(t *testing.T) {
	name := filepath.Join(t.TempDir(), "server.log")
	l := &serviceLog{path: name, limit: 8, keep: 2}
	for i := 0; i < 8; i++ {
		if _, err := l.Write([]byte("12345678")); err != nil {
			t.Fatal(err)
		}
	}
	for _, suffix := range []string{"", ".1", ".2"} {
		info, err := os.Stat(name + suffix)
		if err != nil || info.Size() != 8 || info.Mode().Perm() != 0600 {
			t.Fatal(info, err)
		}
	}
	if _, err := os.Stat(name + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unbounded logs", err)
	}
}

func TestServiceStatusAuthenticatesSetupAndRejectsOtherInstallation(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	info, stop := startTestServer(t, []string{"--home", home}, serverOptions{})
	defer stop()
	config, err := readStartupConfig(filepath.Join(home, "config.toml"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	config.Server.Listen = new(strings.TrimPrefix(info.Endpoint, "http://"))
	data, err := toml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	installationID, err := os.ReadFile(filepath.Join(home, "data/installation-id"))
	if err != nil {
		t.Fatal(err)
	}
	r := serviceRegistration{Home: home, UID: os.Getuid(), Platform: runtime.GOOS, InstallationID: string(installationID)}
	status, err := authenticatedServiceStatus(context.Background(), r)
	if err != nil || status["status"] != "setup_required" || status["authenticated"] != true {
		t.Fatal(status, err)
	}
	r.InstallationID = "some-other-installation"
	if _, err := authenticatedServiceStatus(context.Background(), r); err == nil {
		t.Fatal("attached unknown service")
	}
}

func TestServiceManagedInitializationRequiresLoginAndPreservesExistingAccess(t *testing.T) {
	r := serviceTestRegistration(t)
	c, err := readStartupConfig(filepath.Join(r.Home, "config.toml"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Access.LoginEnabled == nil || !*c.Access.LoginEnabled {
		t.Fatal("managed first use did not enable login")
	}
	if err := prepareFirstAccess(c); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatal(err)
	}
	token, err := readAdminToken(filepath.Join(r.Home, "auth/admin-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeBundleRuntime(filepath.Join(r.Root, "current"), r.Home, filepath.Join(r.Root, "current/bin/dsh")); err != nil {
		t.Fatal(err)
	}
	c, err = readStartupConfig(c.path, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareFirstAccess(c); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(c.path)
	again, _ := readAdminToken(filepath.Join(r.Home, "auth/admin-token"))
	if string(before) != string(after) || token != again {
		t.Fatal("repeated managed initialization changed identity")
	}
}

func TestServiceManagedAccessSettingsKeepLoginAndLoopbackProxyBoundary(t *testing.T) {
	c, token := testAccessConfig(t, t.TempDir())
	c.installationID = "managed-installation"
	a, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	a.document = newSystemSettings(c).configDocument
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	w := accessRequest(h, "POST", "/v1/access/settings", token, a.internal, settingsBody(a, false, false, "", token, ""), nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "后台服务需要登录验证") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = accessRequest(h, "POST", "/v1/access/settings", token, a.internal, settingsBody(a, true, true, "", token, "https://assistant.example.com"), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"login_required":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	loaded, err := readStartupConfig(c.path, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Listen == nil || *loaded.Server.Listen != "127.0.0.1:8088" || len(loaded.Server.TrustedProxies) == 0 {
		t.Fatal("managed remote access bypassed loopback proxy", loaded.Server)
	}
}
