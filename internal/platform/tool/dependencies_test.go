package tool

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalDependencyRepairAndAuthoritativeOverride(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "wrapper")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf ready"), 0700); err != nil {
		t.Fatal(err)
	}
	d := Definition{Executable: wrapper, Dependencies: []ExecutableDependency{{Name: "native-tool", OverrideEnvironment: "TEAMX_TEST_NATIVE_TOOL"}}}
	var out, stderr bytes.Buffer
	p := Process{Definition: d, Output: &out, Error: &stderr}
	if DependenciesAvailable(d) || p.Run(context.Background(), nil) != 127 || out.Len() != 0 {
		t.Fatal("missing dependency reached wrapper")
	}
	native := filepath.Join(dir, "native-tool")
	if err := os.WriteFile(native, []byte("#!/bin/sh\nexit 0"), 0700); err != nil {
		t.Fatal(err)
	}
	if !DependenciesAvailable(d) || p.Run(context.Background(), nil) != 0 || out.String() != "ready" {
		t.Fatal("dependency repair not detected")
	}
	t.Setenv("TEAMX_TEST_NATIVE_TOOL", filepath.Join(dir, "missing-override"))
	if DependenciesAvailable(d) {
		t.Fatal("broken explicit dependency override fell back to sibling")
	}
}
