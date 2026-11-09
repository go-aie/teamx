package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func settingsFromFile(t *testing.T, path string, required bool) *systemSettings {
	t.Helper()
	config, err := loadStartupConfig(path, required)
	if err != nil {
		t.Fatal(err)
	}
	return newSystemSettings(config)
}

func TestSystemSettingsStartup(t *testing.T) {
	for _, name := range []string{defaultTimeZone, "UTC", "America/New_York"} {
		path := writeStartupConfig(t, t.TempDir(), "[server]\ntime_zone = '"+name+"'\n")
		snapshot := settingsFromFile(t, path, true).snapshot()
		if snapshot.TimeZone != name || snapshot.SettingsVersion != 1 || snapshot.location.String() != name {
			t.Fatal(snapshot)
		}
	}
	for _, name := range []string{"", "Local", "Invalid/Zone", " UTC", "/etc/localtime", "../UTC"} {
		path := writeStartupConfig(t, t.TempDir(), "[server]\ntime_zone = '"+name+"'\n")
		if _, err := loadStartupConfig(path, true); err == nil {
			t.Fatal("invalid zone accepted", name)
		}
		var out bytes.Buffer
		if err := serveWithFactory([]string{"--config", path}, &out, &out, func(string, string, serviceConfig) (*service, error) {
			t.Fatal("invalid time zone reached service creation")
			return nil, nil
		}); err == nil {
			t.Fatal("serve accepted invalid zone", name)
		}
	}
	for _, source := range []string{"", "[server]\nlisten = '127.0.0.1:0'\n"} {
		s := settingsFromFile(t, writeStartupConfig(t, t.TempDir(), source), true)
		if s.snapshot().TimeZone != defaultTimeZone {
			t.Fatal("missing time_zone did not default")
		}
	}
}

func TestSystemSettingsPatchPreservesConfig(t *testing.T) {
	for _, source := range []string{
		"",
		"# empty config\n",
		"[server]",
		"[server] # server comment\nlisten = '127.0.0.1:1' # keep\n[env]\nSECRET = 'keep me'\n",
		"[server]\ntime_zone = 'Asia/Shanghai' # local\nsession_max_turns = 2\n",
		"[ 'server' ]\n 'time_zone' = '''Asia/Shanghai''' # local\n",
		"server.time_zone = \"\"\"Asia/Shanghai\"\"\" # local\nserver.session_max_turns = 2\n",
		"server.listen = '127.0.0.1:1'\n",
		"server = { listen = '127.0.0.1:1', time_zone = 'Asia/Shanghai' } # keep\n",
		"server = { listen = '127.0.0.1:1' } # keep\n",
		"server = {}\n",
		"[server.image_materials]\n[env]\nSECRET = '''\n[server]\ntime_zone = 'fake'\n'''\n",
		"[server]\r\nlisten = '127.0.0.1:1' # keep\r\n",
		"[env]\nSECRET = 'keep me'", // No final newline; keep its value out of [server].
	} {
		t.Run(fmt.Sprintf("%q", source), func(t *testing.T) {
			patched, err := patchTimeZone([]byte(source), "America/New_York")
			if err != nil {
				t.Fatal(err)
			}
			var before, after map[string]any
			if toml.Unmarshal([]byte(source), &before) != nil || toml.Unmarshal(patched, &after) != nil {
				t.Fatal("invalid TOML")
			}
			server := after["server"].(map[string]any)
			if server["time_zone"] != "America/New_York" {
				t.Fatal(string(patched))
			}
			delete(server, "time_zone")
			if before == nil {
				before = map[string]any{}
			}
			if old, ok := before["server"].(map[string]any); ok {
				delete(old, "time_zone")
			} else if len(server) == 0 {
				delete(after, "server")
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("unrelated values changed: %v != %v", before, after)
			}
			if strings.Contains(source, "# keep") && !bytes.Contains(patched, []byte("# keep")) {
				t.Fatal("comment lost")
			}
			// Once present, only the value token may change.
			again, err := patchTimeZone(patched, "UTC")
			if err != nil || string(again) != strings.Replace(string(patched), `"America/New_York"`, `"UTC"`, 1) {
				t.Fatal("patch changed other bytes", string(again), err)
			}
		})
	}
}

func TestSystemSettingsSaveAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "config.toml")
	s := settingsFromFile(t, path, false)
	// A no-op must not create even the default config directory or lock file.
	if _, err := s.save(defaultTimeZone, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("no-op touched filesystem", err)
	}
	if view, err := s.save("UTC", 1); err != nil || view != (settingsView{"UTC", 2}) {
		t.Fatal(view, err)
	}
	if view, err := s.save("UTC", 1); !errors.Is(err, ErrConflict) || view.SettingsVersion != 2 {
		t.Fatal("stale no-op accepted", view, err)
	}
	s.ops.createTemp = func(string, string) (snapshotFile, error) { t.Fatal("no-op rewrote config"); return nil, nil }
	if view, err := s.save("UTC", 2); err != nil || view.SettingsVersion != 2 {
		t.Fatal(view, err)
	}
	if got := settingsFromFile(t, path, true).snapshot(); got.TimeZone != "UTC" {
		t.Fatal("restart lost persisted setting", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Fatal("new config permissions", info.Mode())
	}
	if view, err := s.save("Local", 2); err == nil || view.TimeZone != "UTC" {
		t.Fatal("invalid setting changed memory", view, err)
	}
}

type settingsFaultFile struct {
	snapshotFile
	write func([]byte) (int, error)
	sync  func() error
	close func() error
}

func (f settingsFaultFile) Write(p []byte) (int, error) {
	if f.write != nil {
		return f.write(p)
	}
	return f.snapshotFile.Write(p)
}
func (f settingsFaultFile) Sync() error {
	if f.sync != nil {
		return f.sync()
	}
	return f.snapshotFile.Sync()
}
func (f settingsFaultFile) Close() error {
	if f.close != nil {
		return f.close()
	}
	return f.snapshotFile.Close()
}

func TestSystemSettingsWriteFailuresAndReconciliation(t *testing.T) {
	failure := errors.New("injected IO failure")
	for _, boundary := range []string{"mkdir", "create", "write", "sync", "close", "rename-before", "rename-after", "directory-sync"} {
		t.Run(boundary, func(t *testing.T) {
			path := writeStartupConfig(t, t.TempDir(), "# keep\n[server]\nsession_max_turns = 2\n[env]\nSECRET = 'never echo'\n")
			s := settingsFromFile(t, path, true)
			before := bytes.Clone(s.source)
			switch boundary {
			case "mkdir":
				s.ops.makeDir = func(string) error { return failure }
			case "create":
				s.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, failure }
			case "write", "sync", "close":
				create := s.ops.createTemp
				s.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					f, err := create(dir, pattern)
					if err != nil {
						return nil, err
					}
					fault := settingsFaultFile{snapshotFile: f}
					switch boundary {
					case "write":
						fault.write = func([]byte) (int, error) { return 0, failure }
					case "sync":
						fault.sync = func() error { return failure }
					case "close":
						fault.close = func() error { _ = f.Close(); return failure }
					}
					return fault, nil
				}
			case "rename-before":
				s.ops.rename = func(string, string) error { return failure }
			case "rename-after":
				s.ops.rename = func(a, b string) error {
					if err := os.Rename(a, b); err != nil {
						return err
					}
					return failure
				}
			case "directory-sync":
				s.ops.syncDir = func(string) error { return failure }
			}
			view, err := s.save("UTC", 1)
			if boundary == "rename-after" {
				if err != nil || view != (settingsView{"UTC", 2}) {
					t.Fatal("confirmed durable replacement not applied", view, err)
				}
				return
			}
			if err == nil || view != (settingsView{defaultTimeZone, 1}) || s.snapshot().TimeZone != defaultTimeZone {
				t.Fatal("failed save published settings", view, err)
			}
			data, _ := os.ReadFile(path)
			if boundary == "directory-sync" {
				if !errors.Is(err, ErrCommitUnknown) || !bytes.Contains(data, []byte(`"UTC"`)) {
					t.Fatal("uncertain outcome not retained", err)
				}
				s.ops.syncDir = syncDirectory
				// Retrying reconciles disk first, then reports the advanced version.
				view, err = s.save("America/New_York", 1)
				if !errors.Is(err, ErrConflict) || view != (settingsView{"UTC", 2}) {
					t.Fatal(view, err)
				}
			} else if !bytes.Equal(data, before) {
				t.Fatal("failed write damaged original file")
			}
		})
	}
}

func TestSystemSettingsRejectsExternalEdits(t *testing.T) {
	for _, when := range []string{"before", "during", "replaced", "created"} {
		t.Run(when, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if when != "created" {
				writeStartupConfig(t, filepath.Dir(path), "[server]\nsession_max_turns = 2\n")
			}
			s := settingsFromFile(t, path, when != "created")
			external := []byte("[server]\nsession_max_turns = 99 # external edit\n")
			edit := func() {
				if when == "replaced" {
					_ = os.Remove(path)
				}
				if err := os.WriteFile(path, external, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if when == "during" {
				create := s.ops.createTemp
				s.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					f, err := create(dir, pattern)
					return settingsFaultFile{snapshotFile: f, sync: func() error { edit(); return f.Sync() }}, err
				}
			} else {
				edit()
			}
			if view, err := s.save("UTC", 1); !errors.Is(err, ErrConflict) || view.TimeZone != defaultTimeZone {
				t.Fatal("external edit overwritten", view, err)
			}
			if data, _ := os.ReadFile(path); !bytes.Equal(data, external) {
				t.Fatal("external edit lost")
			}
		})
	}
}

func TestSystemSettingsConcurrentVersionAndSnapshot(t *testing.T) {
	s := settingsFromFile(t, writeStartupConfig(t, t.TempDir(), ""), true)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"UTC", "America/New_York"} {
		wg.Go(func() { _, err := s.save(name, 1); results <- err })
	}
	for range 4 {
		wg.Go(func() {
			for range 100 {
				got := s.snapshot()
				if got.location.String() != got.TimeZone {
					t.Error("mixed snapshot", got)
				}
			}
		})
	}
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 || s.snapshot().SettingsVersion != 2 {
		t.Fatal(succeeded, conflicted)
	}
}

func TestSystemSettingsOtherServiceCannotOverwriteConfig(t *testing.T) {
	path := writeStartupConfig(t, t.TempDir(), "[server]\nsession_max_turns = 2\n")
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	first, other := settingsFromFile(t, path, true), settingsFromFile(t, path, true)
	if _, err := first.save("UTC", 1); err != nil {
		t.Fatal(err)
	}
	if view, err := other.save("America/New_York", 1); !errors.Is(err, ErrConflict) || view.TimeZone != defaultTimeZone {
		t.Fatal(view, err)
	}
	if got := settingsFromFile(t, path, true).snapshot().TimeZone; got != "UTC" {
		t.Fatal("second service overwrote config", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0640 {
		t.Fatal("config permissions changed", info.Mode())
	}
}

func TestSystemSettingsUncertainWriteDoesNotOverwriteExternalState(t *testing.T) {
	path := writeStartupConfig(t, t.TempDir(), "")
	s := settingsFromFile(t, path, true)
	s.ops.syncDir = func(string) error { return errors.New("injected sync failure") }
	if _, err := s.save("UTC", 1); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	external := []byte("[server]\ntime_zone = 'America/New_York' # external\n")
	if err := os.WriteFile(path, external, 0600); err != nil {
		t.Fatal(err)
	}
	s.ops.syncDir = syncDirectory
	if view, err := s.save("UTC", 1); !errors.Is(err, ErrCommitUnknown) || view.TimeZone != defaultTimeZone {
		t.Fatal("unrecognized outcome was overwritten", view, err)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, external) {
		t.Fatal("external state lost")
	}
}

func settingsHTTP(handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(data)))
	return w
}

func TestSystemSettingsHTTPAuthorization(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			s := settingsFromFile(t, writeStartupConfig(t, t.TempDir(), ""), true)
			var handler http.Handler
			var info ServiceInfo
			if managed {
				m, err := newRoomManager(t.TempDir(), 4)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = m.close() })
				m.settings, handler, info = s, m, ServiceInfo{ServiceEpoch: m.epoch}
			} else {
				svc, err := newService(t.TempDir(), "settings", serviceConfig{settings: s})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = svc.close(context.Background()) })
				handler, info = svc, svc.info
			}
			for _, caller := range []Caller{{}, {Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}, {Author: Author{Kind: "integration", ID: "gitlab"}}, {Author: Author{Kind: "human", ID: "user"}, MembershipID: "unexpected"}} {
				r := settingsHTTP(handler, "POST", "/v1/settings/save", settingsRequest{info, caller, 1, "UTC"})
				if r.Code != 403 {
					t.Fatal(r.Code, r.Body.String())
				}
			}
			bad := info
			bad.ServiceEpoch = "stale"
			if r := settingsHTTP(handler, "POST", "/v1/settings/save", settingsRequest{bad, humanCaller, 1, "UTC"}); r.Code != 403 {
				t.Fatal(r.Code)
			}
			for _, path := range []string{"/v1/ui", "/v1/settings"} {
				r := settingsHTTP(handler, "GET", path, nil)
				var got settingsView
				if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &got) != nil || got != s.snapshot().settingsView {
					t.Fatal(path, r.Code, r.Body.String())
				}
			}
			request := settingsRequest{info, humanCaller, 1, "UTC"}
			if r := settingsHTTP(handler, "POST", "/v1/settings/save", request); r.Code != 200 {
				t.Fatal(r.Code, r.Body.String())
			}
			r := settingsHTTP(handler, "POST", "/v1/settings/save", request)
			var conflict settingsView
			if r.Code != 409 || json.Unmarshal(r.Body.Bytes(), &conflict) != nil || conflict != (settingsView{"UTC", 2}) {
				t.Fatal(r.Code, r.Body.String())
			}
			request.ExpectedVersion, request.TimeZone = 2, "Local"
			if r := settingsHTTP(handler, "POST", "/v1/settings/save", request); r.Code != 400 {
				t.Fatal(r.Code, r.Body.String())
			}
			cross := httptest.NewRequest("POST", "/v1/settings/save", strings.NewReader("{}"))
			cross.Header.Set("Origin", "http://evil.example")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, cross)
			if w.Code != 403 {
				t.Fatal("cross-origin save accepted")
			}
		})
	}
}

func TestSystemSettingsSharedByManagedRooms(t *testing.T) {
	m := activationTestManager(t)
	m.settings = settingsFromFile(t, writeStartupConfig(t, t.TempDir(), ""), true)
	m.factory = controlledActivationFactory(nil, nil)
	a, b := managerCreate(t, m, "settings-a"), managerCreate(t, m, "settings-b")
	sa, sb := openRoomOK(t, m, a, "settings-open-a"), openRoomOK(t, m, b, "settings-open-b")
	beforeA, beforeB, epoch := sa.info, sb.info, m.epoch
	prior := sa.settings.snapshot()
	roomConfigPath := filepath.Join(m.roomDir(a.RoomID), "room-config.json")
	before, _ := os.ReadFile(roomConfigPath)
	request := settingsRequest{ServiceInfo{ServiceEpoch: m.epoch}, humanCaller, 1, "America/New_York"}
	if r := settingsHTTP(m, "POST", "/v1/settings/save", request); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, svc := range []*service{sa, sb} {
		if svc.settings != m.settings {
			t.Fatal("Room did not share settings provider")
		}
		r := settingsHTTP(svc, "GET", "/v1/info", nil)
		var got settingsView
		if json.Unmarshal(r.Body.Bytes(), &got) != nil || got != (settingsView{"America/New_York", 2}) {
			t.Fatal(r.Body.String())
		}
	}
	if prior.TimeZone != defaultTimeZone || prior.location.String() != defaultTimeZone || prior.SettingsVersion != 1 {
		t.Fatal("saving mutated a previously captured snapshot", prior)
	}
	if sa.info != beforeA || sb.info != beforeB || m.epoch != epoch {
		t.Fatal("time zone changed service identity")
	}
	if after, _ := os.ReadFile(roomConfigPath); !bytes.Equal(before, after) {
		t.Fatal("time zone entered Room persistence")
	}
}
