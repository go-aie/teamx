//go:build unix

package main

import (
	"os"
	"syscall"
)

func credentialOwnedByRoot(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}
