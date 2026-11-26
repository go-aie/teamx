//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Only the system manager is substituted in lifecycle tests. Real listeners,
// owner authentication, archives, backups and release links exercise the CLI.
func serviceStartupFixture(t *testing.T, decorate func(http.Handler) http.Handler) (serviceRegistration, *httptest.Server) {
	t.Helper()
	r := serviceTestRegistration(t)
	r.UserHome = t.TempDir()
	r.Unit = filepath.Join(r.UserHome, "Library/LaunchAgents/com.teamx.server.plist")
	if err := registerServerInstallation(serverLayout{Root: r.Root, Home: r.Home}); err != nil {
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
	unit, err := serviceTemplate(r)
	if err != nil {
		t.Fatal(err)
	}
	serviceTestFile(t, r.Unit, string(unit), 0644)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	c, _ := testAccessConfig(t, r.Home)
	c.Server.Listen = new(listener.Addr().String())
	c.installationID = r.InstallationID
	data, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	serviceTestFile(t, c.path, string(data), 0600)
	access, err := newServerAccess(c, "http://"+listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler = access.wrap(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/healthz":
			json.NewEncoder(w).Encode(map[string]any{"status": "running"})
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{"service_epoch": "test-epoch", "members": []any{}, "operations": []any{}})
		default:
			http.NotFound(w, req)
		}
	}))
	if decorate != nil {
		handler = decorate(handler)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return r, server
}

func TestServicePortCheckAllowsCurrentInstallationAndRequiresReleaseBeforeStart(t *testing.T) {
	r, server := serviceStartupFixture(t, nil)
	if err := checkServicePort(context.Background(), r, true); err != nil {
		t.Fatalf("current installation rejected: %v", err)
	}
	if err := checkServicePort(context.Background(), r, false); err == nil {
		t.Fatal("start accepted an occupied port")
	}
	server.Close()
	if err := checkServicePort(context.Background(), r, false); err != nil {
		t.Fatalf("stopped installation did not release its port: %v", err)
	}
}

func TestServicePortCheckRejectsOtherInstallationWithoutStoppingIt(t *testing.T) {
	r, server := serviceStartupFixture(t, nil)
	r.InstallationID = "different-installation"
	err := checkServicePort(context.Background(), r, true)
	if err == nil || !strings.Contains(err.Error(), "占用") {
		t.Fatalf("foreign installation accepted: %v", err)
	}
	res, err := http.Get(server.URL + "/access")
	if err != nil {
		t.Fatalf("foreign listener was stopped: %v", err)
	}
	res.Body.Close()
}

func TestServiceStartupReturnsPermanentErrorsWithoutPolling(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int64
			r, _ := serviceStartupFixture(t, func(http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.WriteHeader(status)
				})
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := waitServiceStatus(ctx, r)
			var permanent *serviceStartupError
			if !errors.As(err, &permanent) || calls.Load() != 1 || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("permanent error was retried: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
	t.Run("identity", func(t *testing.T) {
		r, _ := serviceStartupFixture(t, nil)
		r.InstallationID = "different-installation"
		_, err := waitServiceStatus(context.Background(), r)
		var permanent *serviceStartupError
		if !errors.As(err, &permanent) {
			t.Fatalf("identity mismatch was not permanent: %v", err)
		}
	})
	t.Run("configuration", func(t *testing.T) {
		r, _ := serviceStartupFixture(t, nil)
		serviceTestFile(t, filepath.Join(r.Home, "config.toml"), "invalid = [", 0600)
		_, err := waitServiceStatus(context.Background(), r)
		var permanent *serviceStartupError
		if !errors.As(err, &permanent) {
			t.Fatalf("invalid configuration was not permanent: %v", err)
		}
	})
}

func TestServiceStartupRetriesTemporaryResponses(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int64
			r, _ := serviceStartupFixture(t, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.URL.Path == "/v1/access/session" && calls.Add(1) == 1 {
						w.WriteHeader(status)
						return
					}
					next.ServeHTTP(w, req)
				})
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			status, err := waitServiceStatus(ctx, r)
			if err != nil || status["authenticated"] != true || calls.Load() != 2 {
				t.Fatalf("temporary response did not recover: calls=%d status=%v err=%v", calls.Load(), status, err)
			}
		})
	}
}

func TestServiceUpgradePreflightPreservesServiceAndSelectionOnPortConflict(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("CLI registration requires a native account and service-unit path; portable endpoint checks run separately")
	}
	r, server := serviceStartupFixture(t, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	})
	before, err := os.Readlink(filepath.Join(r.Root, "current"))
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	runner := func(context.Context, string, ...string) ([]byte, error) { calls++; return nil, nil }
	backup := filepath.Join(filepath.Dir(r.Home), "backup")
	var stdout, stderr bytes.Buffer
	err = runServiceWithManager([]string{"upgrade", "--server-root", r.Root, "--archive", "not-downloaded.tar.gz", "--sha256", strings.Repeat("0", 64), "--backup-dir", backup}, &stdout, &stderr, runner)
	if err == nil || !strings.Contains(err.Error(), "占用") || calls != 0 {
		t.Fatalf("upgrade did not reject conflict before manager/archive access: calls=%d err=%v", calls, err)
	}
	if !strings.Contains(stderr.String(), "尚未停止服务或切换版本") {
		t.Fatal(stderr.String())
	}
	after, err := os.Readlink(filepath.Join(r.Root, "current"))
	if err != nil || before != after {
		t.Fatalf("release changed: %s -> %s: %v", before, after, err)
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup created before conflict was resolved: %v", err)
	}
	res, err := http.Get(server.URL)
	if err != nil {
		t.Fatal("existing listener was interrupted", err)
	}
	res.Body.Close()
}

func TestServiceUpgradeStopsBacksUpAndStartsSameInstallation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("CLI registration requires a native account and service-unit path; portable endpoint checks run separately")
	}
	for _, scenario := range []string{"preserved", "unconfigured", "removed", "changed", "late_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			lateConflict := scenario == "late_conflict"
			r, server := serviceStartupFixture(t, nil)
			catalogPath := filepath.Join(r.Home, "gitlab-applications.json")
			if scenario != "unconfigured" {
				serviceTestFile(t, catalogPath, `{"version":1,"applications":[{"id":"app","origin":"https://gitlab.example","secret":"private-app-secret"}]}`, 0600)
			}
			address := server.Listener.Addr().String()
			handler := server.Config.Handler
			archive, digest := serviceTestArchive(t, serviceTestRelease(t, "dev-new"))
			var phases []string
			var started *httptest.Server
			running := true
			runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
				switch args[0] {
				case "print":
					if running {
						return []byte("state = running"), nil
					}
					return nil, errors.New("not loaded")
				case "bootout":
					phases = append(phases, "stop")
					server.Close()
					running = false
				case "bootstrap":
					phases = append(phases, "start")
					if scenario == "removed" {
						if err := os.Remove(catalogPath); err != nil {
							return nil, err
						}
					} else if scenario == "changed" {
						serviceTestFile(t, catalogPath, `{"version":1,"applications":[]}`, 0600)
					}
					selected, err := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
					if err != nil || !strings.Contains(selected, "dev-new") {
						return nil, fmt.Errorf("start before selecting new release: %s: %v", selected, err)
					}
					listener, err := net.Listen("tcp", address)
					if err != nil {
						return nil, err
					}
					var nextHandler http.Handler = handler
					if lateConflict {
						nextHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
					}
					started = httptest.NewUnstartedServer(nextHandler)
					started.Listener = listener
					started.Start()
					t.Cleanup(started.Close)
					running = true
				}
				return nil, nil
			}
			var stdout, stderr bytes.Buffer
			err := runServiceWithManager([]string{"upgrade", "--server-root", r.Root, "--archive", archive, "--sha256", digest}, &stdout, &stderr, runner)
			if scenario == "removed" || scenario == "changed" {
				if err == nil || !strings.Contains(err.Error(), "GitLab") || stdout.Len() != 0 || strings.Join(phases, ",") != "stop,start" {
					t.Fatalf("configuration loss reported as successful upgrade: phases=%v err=%v", phases, err)
				}
				if strings.Contains(stderr.String(), "private-app-secret") || strings.Contains(err.Error(), "private-app-secret") {
					t.Fatal("upgrade diagnostic leaked the catalog")
				}
			} else if lateConflict {
				var permanent *serviceStartupError
				if !errors.As(err, &permanent) || !strings.Contains(err.Error(), "HTTP 401") || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("late conflict was not reported immediately: %v", err)
				}
				if strings.Join(phases, ",") != "stop,start,stop" {
					t.Fatalf("failed managed job was not stopped: %v", phases)
				}
				res, err := http.Get(started.URL)
				if err != nil {
					t.Fatal("foreign listener was terminated", err)
				}
				res.Body.Close()
			} else {
				if err != nil || strings.Join(phases, ",") != "stop,start" {
					t.Fatalf("managed upgrade failed: phases=%v err=%v\n%s", phases, err, stderr.String())
				}
				var status map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status["authenticated"] != true || status["ready"] != true {
					t.Fatalf("new service did not authenticate and recover: %s: %v", stdout.String(), err)
				}
			}
			selected, err := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
			if err != nil || !strings.Contains(selected, "dev-new") {
				t.Fatalf("new selection was lost: %s: %v", selected, err)
			}
			entries, err := os.ReadDir(r.Home + "-backups")
			entries = slices.DeleteFunc(entries, func(entry os.DirEntry) bool { return strings.HasPrefix(entry.Name(), ".") })
			if err != nil || len(entries) != 1 {
				t.Fatalf("automatic backup missing: %v: %v", entries, err)
			}
			if _, err := os.Stat(filepath.Join(r.Home+"-backups", entries[0].Name(), "backup.json")); err != nil {
				t.Fatal("consistent backup missing", err)
			}
			previous := -1
			for _, label := range []string{"校验并准备升级包", "准备执行环境", "登记备份程序归档", "停止服务", "等待服务退出并锁定数据", "备份：流式归档数据与登记信息并同步落盘", "备份：顺序回读并校验归档", "配置执行环境并切换版本", "启动服务", "等待认证控制面就绪", "停服流程耗时"} {
				index := strings.Index(stderr.String(), "[service] "+label)
				if index <= previous {
					t.Fatalf("missing or unordered progress %q: %s", label, stderr.String())
				}
				previous = index
			}
		})
	}
}
