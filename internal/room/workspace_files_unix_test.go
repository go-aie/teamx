//go:build darwin || linux

package room

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWorkspaceFilesRejectRealFIFOAndProtectedSymlinks(t *testing.T) {
	f, dir := fileRootFixture(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		if _, err := f.preview(t.Context(), "pipe"); err == nil {
			done <- nil
			return
		}
		_, err := f.content(t.Context(), "pipe", true)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked read")
	}
	// An ordinary child becomes a protected directory through a link. Direct
	// requests and every intermediate traversal must both refuse it.
	writeFileFixture(t, filepath.Join(dir, "private/secret"), []byte("SECRET"))
	f.protected = []string{filepath.Join(dir, "private")}
	if err := os.Symlink("private", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.preview(t.Context(), "alias/secret"); err == nil {
		t.Fatal("protected alias admitted")
	}
}

func TestWorkspaceFilesSymlinkSwapNeverDeliversProtectedBytes(t *testing.T) {
	f, dir := fileRootFixture(t)
	writeFileFixture(t, filepath.Join(dir, "safe/note.txt"), []byte("SAFE"))
	writeFileFixture(t, filepath.Join(dir, ".teamx/note.txt"), []byte("SECRET"))
	if err := os.Symlink(filepath.Join(dir, "safe/note.txt"), filepath.Join(dir, "absolute")); err != nil {
		t.Fatal(err)
	}
	if p, err := f.preview(t.Context(), "absolute"); err != nil || p.Text != "SAFE" {
		t.Fatal(p, err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.Remove(filepath.Join(dir, "link"))
			os.Symlink(".teamx", filepath.Join(dir, "link"))
			os.Remove(filepath.Join(dir, "link"))
			os.Symlink("safe", filepath.Join(dir, "link"))
		}
	}()
	defer func() { close(stop); <-done }()
	for i := 0; i < 200; i++ {
		p, err := f.preview(t.Context(), "link/note.txt")
		if err == nil && p.Text != "SAFE" {
			t.Fatal("protected bytes delivered", p)
		}
	}
}

func TestWorkspaceFilesSymlinkParentsResolveAfterIntermediateLinks(t *testing.T) {
	f, dir := fileRootFixture(t)
	writeFileFixture(t, filepath.Join(dir, "deep/child/marker"), []byte("child"))
	writeFileFixture(t, filepath.Join(dir, "deep/note.txt"), []byte("ACTUAL"))
	writeFileFixture(t, filepath.Join(dir, "note.txt"), []byte("WRONG"))
	for name, target := range map[string]string{"child-link": "deep/child", "relative": "child-link/../note.txt", "absolute": filepath.Join(dir, "child-link/../note.txt")} {
		// filepath.Join cleans parents; construct the absolute target verbatim.
		if name == "absolute" {
			target = dir + "/child-link/../note.txt"
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"relative", "absolute"} {
		p, err := f.preview(t.Context(), name)
		if err != nil || p.Text != "ACTUAL" {
			t.Fatal(name, p, err)
		}
	}
}
