//go:build linux

package srt

import "testing"

func TestWorkspaceMountAliasesAreRejected(t *testing.T) {
	root := "1 0 8:1 / / rw - ext4 /dev/test rw\n"
	if err := checkMountInfo("/work/a", root); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{
		"2 1 8:1 /private /work rw - ext4 /dev/test rw\n",
		"2 1 8:1 / /work rw - ext4 /dev/test rw\n",
		"2 1 9:1 / /work/a/child rw - ext4 /dev/test rw\n",
	} {
		if err := checkMountInfo("/work/a", root+extra); err == nil {
			t.Fatal("mount exposure admitted", extra)
		}
	}
	if err := checkMountInfo("/work/a", "invalid"); err == nil {
		t.Fatal("lost mount observation admitted")
	}
}
