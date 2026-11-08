//go:build (integration || e2e) && unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeArgumentsStreamsAndExitCodes(t *testing.T) {
	args := []string{"--json", "-C", "directory with spaces", "review", "comment", "12", "--help", "--", "line 1\nline 2", "$(never-execute)", ""}
	for _, code := range []int{0, 1, 2, 3, 4, 5, 42} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			bin := fakeScn(t, pinnedVersionScript, "printf '%s\\000' \"$@\"\ncat\nprintf 'native error\\000\\n' >&2\nexit "+strconv.Itoa(code))
			a, out, stderr := testApp(bin)
			a.in = strings.NewReader("stdin\x00bytes\n")
			if got := a.run(context.Background(), args); got != code {
				t.Fatalf("exit = %d, want %d: %s", got, code, stderr)
			}
			want := strings.Join(args, "\x00") + "\x00stdin\x00bytes\n"
			if out.String() != want || stderr.String() != "native error\x00\n" {
				t.Fatalf("streams changed: stdout=%q stderr=%q", out, stderr)
			}
		})
	}
}

func TestVersionGate(t *testing.T) {
	for _, tc := range []struct{ name, version string }{
		{"wrong", "printf 'scn 0.1.14\\n'; exit 0"},
		{"extra-output", "printf 'scn 0.1.13 unexpected\\n'; exit 0"},
		{"failed", "printf 'secret-native-probe-error' >&2; exit 5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, out, stderr := testApp(fakeScn(t, tc.version, "printf 'BUSINESS EXECUTED'"))
			if code := a.run(context.Background(), []string{"review", "create"}); code != 127 || out.Len() != 0 || !strings.Contains(stderr.String(), "require scn 0.1.13") || strings.Contains(stderr.String(), "secret") {
				t.Fatalf("version gate: code=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
	for _, bin := range []string{filepath.Join(t.TempDir(), "missing"), "relative/scn"} {
		a, out, stderr := testApp(bin)
		// A working PATH candidate cannot replace an invalid explicit override.
		a.environ = append(a.environ, "PATH="+filepath.Dir(fakeScn(t, pinnedVersionScript, "echo WRONG")))
		if code := a.run(context.Background(), []string{"--help"}); code != 127 || out.Len() != 0 {
			t.Fatalf("broken override fell back: code=%d out=%q stderr=%q", code, out, stderr)
		}
	}
}

func TestResolutionOrder(t *testing.T) {
	sibling := fakeScn(t, pinnedVersionScript, "exit 0")
	onPath := fakeScn(t, pinnedVersionScript, "exit 0")
	self := func() (string, error) { return filepath.Join(filepath.Dir(sibling), "teamx-scion"), nil }
	for _, tc := range []struct{ override, path, want string }{
		{onPath, filepath.Dir(onPath), onPath},
		{"", filepath.Dir(onPath), sibling},
	} {
		got, err := resolveScn(tc.override, self, tc.path)
		if err != nil || got != tc.want {
			t.Fatalf("resolution: got=%q err=%v want=%q", got, err, tc.want)
		}
	}
	missingSelf := func() (string, error) { return filepath.Join(t.TempDir(), "teamx-scion"), nil }
	if got, err := resolveScn("", missingSelf, filepath.Dir(onPath)); err != nil || got != onPath {
		t.Fatalf("PATH: got=%q err=%v", got, err)
	}
	if _, err := resolveScn("", missingSelf, ".:relative/path"); err == nil {
		t.Fatal("relative PATH must not select a workspace executable")
	}
}

func TestEnvironmentIsolationAndPrivateCleanup(t *testing.T) {
	// A forbidden source variable cannot even reach the version probe.
	// Use a separate probe script to record via its own hardcoded test path.
	probeFile := filepath.Join(t.TempDir(), "probe-env")
	bin := fakeScn(t, "env > "+shellQuote(probeFile)+"\n"+pinnedVersionScript, "env")
	a, out, stderr := testApp(bin)
	profile := filepath.Join(t.TempDir(), "credentials.json")
	a.environ = append(a.environ,
		"HOME=/operator/home", "XDG_CONFIG_HOME=/operator/config", "SCION_CREDENTIALS=keychain",
		"SCION_CREDENTIALS_FILE="+profile, "SCION_TOKEN=global-secret", "SCION_CONFIG_FILE=/operator/config.json",
		"SCION_NO_DAEMON=0", "SCION_DAEMON_SOCKET=/operator/daemon", "SCION_LOG=trace", "SCION_UPDATE_URL=untrusted",
		"SCION_ENDPOINT=https://configured.example", "SCION_HUB=https://configured.example", "SCION_REPO=org/repo", "SCION_AUTHOR=Developer <dev@example.com>",
		"OPENAI_API_KEY=model-secret", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=operator",
		"GITLAB_TOKEN=other-secret", "SSH_AUTH_SOCK=/operator/ssh", "SCION_PROBE_ENV=forbidden")
	if code := a.run(context.Background(), []string{"status"}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	child := parseEnv(out.String())
	probeBytes, err := os.ReadFile(probeFile)
	if err != nil {
		t.Fatal(err)
	}
	probe := parseEnv(string(probeBytes))
	for label, env := range map[string]map[string]string{"child": child, "probe": probe} {
		for _, key := range []string{"SCION_TOKEN", "SCION_DAEMON_SOCKET", "SCION_LOG", "SCION_UPDATE_URL", "GIT_CONFIG_COUNT", "GITLAB_TOKEN", "OPENAI_API_KEY", "SSH_AUTH_SOCK", "SCION_PROBE_ENV"} {
			if _, ok := env[key]; ok {
				t.Errorf("%s inherited %s", label, key)
			}
		}
		if env["SCION_CREDENTIALS"] != "file" || env["SCION_NO_DAEMON"] != "1" || env["GIT_CONFIG_GLOBAL"] != os.DevNull || env["HOME"] == "/operator/home" || env["SCION_CONFIG_FILE"] == "/operator/config.json" {
			t.Errorf("%s isolation failed", label)
		}
		if _, err := os.Stat(env["HOME"]); !os.IsNotExist(err) {
			t.Errorf("private HOME was not removed: %v", err)
		}
	}
	if child["SCION_CREDENTIALS_FILE"] != profile || child["SCION_AUTHOR"] != "Developer <dev@example.com>" || child["SCION_HUB"] != "https://configured.example" || child["SCION_REPO"] != "org/repo" {
		t.Fatal("explicit runtime inputs lost")
	}
	if probe["SCION_CREDENTIALS_FILE"] == profile || probe["SCION_ENDPOINT"] != "" || probe["SCION_AUTHOR"] != "" {
		t.Fatal("version probe used runtime profile or endpoint")
	}
}

func TestCancellationSettlesProcessGroup(t *testing.T) {
	for _, stage := range []string{"probe", "command"} {
		t.Run(stage, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			blocking := "sleep 60 &\nchild=$!\nprintf '%s %s\\n' \"$$\" \"$child\" > " + shellQuote(ready) + "\nwait"
			version, body := pinnedVersionScript, blocking
			if stage == "probe" {
				version, body = blocking, "echo SHOULD-NOT-RUN"
			}
			a, out, stderr := testApp(fakeScn(t, version, body))
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			done := make(chan int, 1)
			go func() { done <- a.run(ctx, []string{"review", "create"}) }()
			fields := strings.Fields(string(waitForFile(t, ready)))
			cancel()
			select {
			case code := <-done:
				if code != 130 || out.Len() != 0 {
					t.Fatalf("cancel code=%d out=%q stderr=%q", code, out, stderr)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("cancellation did not finish")
			}
			for _, field := range fields {
				pid, err := strconv.Atoi(field)
				if err != nil {
					t.Fatal(err)
				}
				assertGone(t, pid)
			}
		})
	}
}

func TestExitedParentStillSettlesDescendants(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	a, _, stderr := testApp(fakeScn(t, pinnedVersionScript, "sleep 60 >/dev/null 2>&1 &\nprintf '%s\\n' \"$!\" > "+shellQuote(ready)+"\nexit 4"))
	if code := a.run(context.Background(), []string{"status"}); code != 4 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(waitForFile(t, ready))))
	if err != nil {
		t.Fatal(err)
	}
	assertGone(t, pid)
}

func TestNoControllingTerminal(t *testing.T) {
	a, out, stderr := testApp(fakeScn(t, pinnedVersionScript, "if (exec 3</dev/tty) 2>/dev/null; then exit 99; fi\nprintf 'no controlling terminal\\n'"))
	if code := a.run(context.Background(), []string{"--help"}); code != 0 || out.String() != "no controlling terminal\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}
