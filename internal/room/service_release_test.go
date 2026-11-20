//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func serviceUpdateFixture(t *testing.T, current, target, failure string) (serviceRegistration, serviceReleaseClient, func() []string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := serviceRegistration{Root: filepath.Join(parent, "server"), Home: filepath.Join(parent, "private"), Platform: runtime.GOOS}
	old := filepath.Join(r.Root, "releases", "original")
	serviceTestFile(t, filepath.Join(old, "manifest.json"), fmt.Sprintf(`{"version":%q,"profile":"standard","target":%q}`, current, runtime.GOOS+"-"+runtime.GOARCH), 0644)
	serviceTestFile(t, filepath.Join(r.Home, "unchanged"), "original data", 0600)
	if err := os.Symlink("releases/original", filepath.Join(r.Root, "current")); err != nil {
		t.Fatal(err)
	}
	archiveVersion := target
	if failure == "wrong-manifest" {
		archiveVersion = "v8.0.0"
	}
	archive, digest := serviceTestArchive(t, serviceTestRelease(t, archiveVersion))
	body, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	name := "teamx-" + target + "-" + runtime.GOOS + "-" + runtime.GOARCH + "-standard.tar.gz"
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		paths = append(paths, req.URL.Path)
		mu.Unlock()
		switch req.URL.Path {
		case "/releases/latest", "/releases/tags/" + target:
			switch failure {
			case "network":
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			case "prerelease":
				fmt.Fprintf(w, `{"tag_name":%q,"prerelease":true}`, target)
			case "draft":
				fmt.Fprintf(w, `{"tag_name":%q,"draft":true}`, target)
			case "bad-json":
				fmt.Fprint(w, "broken")
			case "wrong-version":
				fmt.Fprint(w, `{"tag_name":"v9.9.9"}`)
			default:
				fmt.Fprintf(w, `{"tag_name":%q}`, target)
			}
		case "/downloads/" + target + "/SHA256SUMS":
			if failure == "missing-digest" {
				fmt.Fprint(w, "unrelated")
				return
			}
			fmt.Fprintf(w, "%s  %s\n", digest, name)
			if failure == "duplicate-digest" {
				fmt.Fprintf(w, "%s  %s\n", digest, name)
			}
		case "/downloads/" + target + "/" + name:
			if failure == "missing-archive" {
				http.NotFound(w, req)
				return
			}
			if failure == "checksum" {
				fmt.Fprint(w, "tampered")
				return
			}
			w.Write(body)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	client := serviceReleaseClient{http: server.Client(), apiRoot: server.URL + "/releases", downloadRoot: server.URL + "/downloads"}
	return r, client, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), paths...) }
}

func assertServiceUpdateUnchanged(t *testing.T, r serviceRegistration) {
	t.Helper()
	if link, err := os.Readlink(filepath.Join(r.Root, "current")); err != nil || link != "releases/original" {
		t.Fatalf("current changed: %s %v", link, err)
	}
	if body, err := os.ReadFile(filepath.Join(r.Home, "unchanged")); err != nil || string(body) != "original data" {
		t.Fatalf("home changed: %s %v", body, err)
	}
	entries, err := os.ReadDir(r.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".download-") || strings.HasPrefix(entry.Name(), ".unpack-") {
			t.Fatalf("temporary download leaked: %s", entry.Name())
		}
	}
}

func TestServiceOnlineUpgradePinsReleaseAndVerifiesBeforeSelection(t *testing.T) {
	for _, version := range []string{"", "latest", "v1.2.3"} {
		t.Run("version="+version, func(t *testing.T) {
			r, client, requests := serviceUpdateFixture(t, "v1.0.0", "v1.2.3", "")
			var output bytes.Buffer
			next, err := prepareServiceOnlineUpgrade(context.Background(), r, version, &output, client)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := verifyServiceRelease(next); err != nil || got != "v1.2.3-"+runtime.GOOS+"-"+runtime.GOARCH+"-standard" {
				t.Fatalf("release = %s: %v", got, err)
			}
			endpoint := "/releases/latest"
			if version == "v1.2.3" {
				endpoint = "/releases/tags/v1.2.3"
			}
			want := []string{endpoint, "/downloads/v1.2.3/SHA256SUMS", "/downloads/v1.2.3/teamx-v1.2.3-" + runtime.GOOS + "-" + runtime.GOARCH + "-standard.tar.gz"}
			if !reflect.DeepEqual(requests(), want) {
				t.Fatalf("requests: %v", requests())
			}
			if !strings.Contains(output.String(), "v1.0.0 → v1.2.3") {
				t.Fatal(output.String())
			}
			assertServiceUpdateUnchanged(t, r)
		})
	}
}

func TestServiceOnlineUpgradeNoopAndFailuresLeaveCurrentAndHomeUntouched(t *testing.T) {
	for _, scenario := range []string{"same", "older", "network", "prerelease", "draft", "bad-json", "wrong-version", "wrong-manifest", "missing-digest", "duplicate-digest", "missing-archive", "checksum", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			current := "v1.0.0"
			if scenario == "same" {
				current = "v1.2.3"
			}
			if scenario == "older" {
				current = "v1.3.0"
			}
			r, client, requests := serviceUpdateFixture(t, current, "v1.2.3", scenario)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			var output bytes.Buffer
			next, err := prepareServiceOnlineUpgrade(ctx, r, "v1.2.3", &output, client)
			if next != "" || (scenario == "same" && err != nil) || (scenario != "same" && err == nil) {
				t.Fatalf("next=%q err=%v", next, err)
			}
			if scenario == "same" || scenario == "older" {
				if len(requests()) != 1 {
					t.Fatalf("unexpected download: %v", requests())
				}
			}
			assertServiceUpdateUnchanged(t, r)
		})
	}
}

func TestServiceStableVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		a, b   string
		want   int
		stable bool
	}{
		{"v0.0.3", "v0.0.3", 0, true}, {"v0.0.9", "v0.0.10", -1, true},
		{"v1.0.0", "v0.100.100", 1, true}, {"v99999999999999999999999.0.0", "v2.0.0", 1, true},
		{"dev", "v0.0.3", 0, false}, {"v01.0.0", "v1.0.0", 0, false}, {"v1.0.0-rc.1", "v1.0.0", 0, false},
	} {
		got, stable := compareServiceVersions(tc.a, tc.b)
		if stable != tc.stable || got != tc.want {
			t.Fatalf("%s vs %s: %d %v", tc.a, tc.b, got, stable)
		}
	}
}

func TestServiceBackupDestinationPreflight(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := serviceRegistration{Root: filepath.Join(parent, "server"), Home: filepath.Join(parent, "home")}
	for _, dir := range []string{r.Root, r.Home, filepath.Join(parent, "backups")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(r.Home, filepath.Join(parent, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path  string
		valid bool
	}{
		{filepath.Join(parent, "backups", "new"), true}, {filepath.Join(parent, "backups"), false},
		{filepath.Join(parent, "missing", "new"), false}, {filepath.Join(r.Home, "new"), false},
		{filepath.Join(r.Root, "new"), false}, {parent, false}, {filepath.Join(parent, "alias", "new"), false},
	} {
		if err := validateServiceBackupDestination(r, tc.path); (err == nil) != tc.valid {
			t.Fatalf("%s valid=%v: %v", tc.path, tc.valid, err)
		}
	}
	a, err := defaultServiceUpgradeBackup(r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := defaultServiceUpgradeBackup(r)
	if err != nil || a == b {
		t.Fatalf("backup paths must differ: %s %s: %v", a, b, err)
	}
	if err := validateServiceBackupDestination(r, a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Fatalf("preflight created backup destination: %v", err)
	}
}

func TestServiceCommandHelpAndInvalidArgumentsAreLocal(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"service"}} {
		var out, diagnostics bytes.Buffer
		if err := Run(args, &out, &diagnostics); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "upgrade") {
			t.Fatal(out.String())
		}
	}
	for _, action := range []string{"upgrade", "logs", "status", "backup", "restore"} {
		var out, diagnostics bytes.Buffer
		if err := runService([]string{action, "--help"}, &out, &diagnostics); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Usage: teamx service "+action) {
			t.Fatal(out.String())
		}
		for _, unrelated := range []string{"-user ", "-home "} {
			if strings.Contains(out.String(), unrelated) {
				t.Fatalf("%s exposes %s", action, unrelated)
			}
		}
		if action == "upgrade" && strings.Contains(out.String(), "-from ") {
			t.Fatal(out.String())
		}
	}
	for _, args := range [][]string{
		{"upgrade", "--version", "v1.0.0-rc.1"}, {"upgrade", "--version", "v1.0.0", "--archive", "unused"},
		{"upgrade", "--sha256", "unused"}, {"upgrade", "--home", "unused"}, {"status", "--archive", "unused"},
		{"logs", "--lines", "0"}, {"upgrade", "unexpected"}, {"unknown"}, {"backup"}, {"restore"},
	} {
		var out, diagnostics bytes.Buffer
		err := runService(args, &out, &diagnostics)
		if err == nil || strings.Contains(err.Error(), "invalid room request") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestServiceLogsReadsNativeFile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS file log reader")
	}
	r := serviceRegistration{Platform: "darwin", LogRoot: t.TempDir()}
	serviceTestFile(t, filepath.Join(r.LogRoot, "server.log"), "first\nlast\n", 0600)
	var out, diagnostics bytes.Buffer
	if err := writeServiceLogs(context.Background(), r, 1, false, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if out.String() != "last\n" {
		t.Fatal(out.String())
	}
}

func TestServiceLogsDoesNotAcquireMaintenanceLock(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS file log reader")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := serviceRegistration{Version: 1, InstallationID: "fixture", Root: filepath.Join(parent, "server"), Home: filepath.Join(parent, "home"), UserHome: filepath.Join(parent, "account"), UID: os.Getuid(), Platform: "darwin", LogRoot: filepath.Join(parent, "logs")}
	r.Unit = filepath.Join(r.UserHome, "Library/LaunchAgents/com.teamx.server.plist")
	serviceTestFile(t, filepath.Join(r.LogRoot, "server.log"), "entry\n", 0600)
	if err := os.MkdirAll(r.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeServiceJSON(filepath.Join(r.Root, "service.json"), r); err != nil {
		t.Fatal(err)
	}
	if err := writeServiceJSON(filepath.Join(r.Root, "installation.json"), serverInstallation{Version: 1, InstanceID: r.InstallationID, Home: r.Home, ServerRoot: r.Root}); err != nil {
		t.Fatal(err)
	}
	lock, err := lockDirectoryFile(r.Root, ".service.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlockDirectory(lock)
	var out, diagnostics bytes.Buffer
	if err := runService([]string{"logs", "--server-root", r.Root}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if out.String() != "entry\n" {
		t.Fatal(out.String())
	}
}
