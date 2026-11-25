//go:build linux

package srt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedRuntimeChecksOwnershipWriteAccessAndAncestors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runtime protection is evaluated as the ordinary service account")
	}
	// A packaged, root-owned system executable is immutable to the service.
	path := "/usr/bin/true"
	want, err := DigestPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := protectedDigest(path); err != nil || got != want {
		t.Fatal("protected bytes changed their recovery digest", got, err)
	}
	// 0444 does not make a service-owned file immutable: its owner can chmod it.
	file := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(file, []byte("owned"), 0444); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkProtectedPath(file, info); err == nil {
		t.Fatal("service-owned read-only file accepted")
	}
	for _, writable := range []string{"/tmp", "/dev/null"} {
		info, err := os.Stat(writable)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkProtectedPath(writable, info); err == nil {
			t.Fatal("writable root-owned path accepted", writable)
		}
	}
	// Following a protected executable through a replaceable parent is unsafe.
	if err := os.Symlink(path, file+"-link"); err != nil {
		t.Fatal(err)
	}
	if _, err := protectedDigest(file + "-link"); err == nil {
		t.Fatal("replaceable runtime pathname accepted")
	}
}
