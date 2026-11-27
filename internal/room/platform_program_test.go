package room

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestLarkProgramSourceAndPersonalCredentialBackend(t *testing.T) {
	release, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := platform.CLIProfile{Version: 1, Executable: "/removed-release/lark-cli", Profile: "fixture", Brand: "feishu", CLIVersion: lark.CLIVersion}
	for _, managed := range []bool{false, true} {
		m := &roomManager{config: serviceConfig{environment: []string{"PATH=/not-installed", "TEAMX_LARK_BIN=" + filepath.Join(release, "custom-cli")}}}
		if managed {
			m.config.platformBinDir = release
		}
		selected, err := m.platformProgram("lark", &original)
		want := original.Executable
		if managed {
			want = filepath.Join(release, "custom-cli")
		}
		if err != nil || selected != want {
			t.Fatal(managed, selected, err)
		}
		if err := m.configureLarkRuntime(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if m.larkClients.ManagedProgram() != managed {
			t.Fatal("wrong program authority")
		}
		if m.larkClients.Executable(original) != want {
			t.Fatal("factory lost service selection")
		}
		// Program binding in an installed personal service keeps legacy native
		// credentials: opening an unprovisioned external profile must remain possible.
		if _, err := m.larkClients.New(original); err != nil {
			t.Fatal("personal service changed credential backend", err)
		}
	}
}

func TestGitlabProgramPairAndEnvironment(t *testing.T) {
	release, custom, pathDir := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(dir, name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
		physical, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return physical
	}
	wrapper, native := write(release, "teamx-gitlab"), write(release, "glab")
	customWrapper, customNative := write(custom, "teamx-gitlab"), write(custom, "glab")
	pathWrapper, pathNative := write(pathDir, "teamx-gitlab"), write(pathDir, "glab")
	missingNativeDir := t.TempDir()
	write(missingNativeDir, "teamx-gitlab")
	brokenNative := write(t.TempDir(), "glab")
	if err := os.Chmod(brokenNative, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, binDir, override, nativeOverride, wrapper, native string
		unavailable                                             bool
	}{
		{name: "installed", binDir: release, wrapper: wrapper, native: native},
		{name: "standalone", wrapper: pathWrapper, native: pathNative},
		{name: "wrapper override owns sibling", binDir: release, override: customWrapper, wrapper: customWrapper, native: customNative},
		{name: "native override", binDir: release, nativeOverride: customNative, wrapper: wrapper, native: customNative},
		{name: "missing wrapper", binDir: t.TempDir(), unavailable: true},
		{name: "missing installed native", binDir: missingNativeDir, unavailable: true},
		{name: "nonexecutable native override", binDir: release, nativeOverride: brokenNative, unavailable: true},
		{name: "missing native override", binDir: release, nativeOverride: filepath.Join(custom, "missing"), unavailable: true},
		{name: "relative wrapper override", binDir: release, override: "teamx-gitlab", unavailable: true},
		{name: "relative native override", binDir: release, nativeOverride: "glab", unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{"PATH=" + pathDir, "TEAMX_GITLAB_BIN=" + tc.override, "TEAMX_GLAB_BIN=" + tc.nativeOverride, "TEAMX_GLAB_BIN=/must-be-replaced", "PRIVATE_SECRET=never-delegate"}
			before := slices.Clone(env)
			m := &roomManager{platformCatalog: defaultPlatformCatalog(), config: serviceConfig{platformBinDir: tc.binDir, environment: env}}
			for _, restricted := range []bool{false, true} {
				m.config.hostTools.restricted = restricted
				d, err := m.toolDefinition("gitlab")
				if err != nil {
					t.Fatal(err)
				}
				available := tool.ExecutableAvailable(d.Executable) && tool.DependenciesAvailable(d)
				if available == tc.unavailable {
					t.Fatal("incorrect availability", restricted, available)
				}
				if tc.unavailable {
					continue
				}
				if d.Executable != tc.wrapper || environmentValue(d.ProcessEnvironment, "TEAMX_GLAB_BIN") != tc.native {
					t.Fatal("program pair diverged", d.Executable, d.ProcessEnvironment)
				}
				count := 0
				for _, entry := range d.ProcessEnvironment {
					if strings.HasPrefix(entry, "TEAMX_GLAB_BIN=") {
						count++
					}
				}
				if count != 1 || !slices.Equal(env, before) {
					t.Fatal("environment duplicated or source mutated")
				}
			}
		})
	}
}

func TestScionProgramSourcesAndNativeDependencies(t *testing.T) {
	write := func(dir, name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
		p, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	release, other := t.TempDir(), t.TempDir()
	wrapper, native := write(release, "teamx-scion"), write(release, "scn")
	customWrapper, customNative := write(other, "teamx-scion"), write(other, "scn")
	for _, tc := range []struct {
		name, binDir, override, nativeOverride, selected, dependency string
		original                                                     *platform.CLIProfile
		unavailable                                                  bool
	}{
		{name: "installed", binDir: release, selected: wrapper, dependency: native, original: &platform.CLIProfile{Executable: "/removed/scn"}},
		{name: "wrapper override", binDir: release, override: customWrapper, selected: customWrapper, dependency: customNative},
		{name: "native override", binDir: release, nativeOverride: customNative, selected: wrapper, dependency: customNative},
		{name: "direct native", binDir: release, override: customNative, selected: customNative},
		{name: "external first profile", original: &platform.CLIProfile{Executable: native}, selected: native},
		{name: "external second profile", original: &platform.CLIProfile{Executable: customNative}, selected: customNative},
		{name: "external wrapper", original: &platform.CLIProfile{Executable: customWrapper}, selected: customWrapper, dependency: customNative},
		{name: "missing installed", binDir: t.TempDir(), unavailable: true},
		{name: "invalid override", binDir: release, override: "relative/scn", unavailable: true},
		{name: "invalid native override", binDir: release, nativeOverride: "relative/scn", unavailable: true},
		{name: "missing native override", binDir: release, nativeOverride: filepath.Join(other, "missing"), unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{"PATH=" + other, "TEAMX_SCION_BIN=" + tc.override, "TEAMX_SCN_BIN=" + tc.nativeOverride, "TEAMX_SCN_BIN=/duplicate"}
			m := &roomManager{config: serviceConfig{platformBinDir: tc.binDir, environment: env}}
			d, err := m.scionProgram(tc.original)
			available := err == nil && tool.ExecutableAvailable(d.Executable) && tool.DependenciesAvailable(d)
			if available == tc.unavailable {
				t.Fatal(available, err, d)
			}
			if tc.unavailable {
				return
			}
			if d.Executable != tc.selected {
				t.Fatal(d.Executable, tc.selected)
			}
			if tc.dependency == "" {
				if len(d.Dependencies) != 0 {
					t.Fatal("native requires wrapper", d.Dependencies)
				}
			} else {
				count := 0
				for _, v := range d.ProcessEnvironment {
					if strings.HasPrefix(v, "TEAMX_SCN_BIN=") {
						count++
					}
				}
				if count != 1 || environmentValue(d.ProcessEnvironment, "TEAMX_SCN_BIN") != tc.dependency {
					t.Fatal("dependency selection diverged", d.ProcessEnvironment)
				}
			}
		})
	}
	// Standalone default discovery may choose native scn without a wrapper.
	m := &roomManager{config: serviceConfig{environment: []string{"PATH=/not-installed", "TEAMX_SCN_BIN=" + native}}}
	if d, err := m.scionProgram(nil); err != nil || d.Executable != native || len(d.Dependencies) != 0 {
		t.Fatal(d, err)
	}
}
