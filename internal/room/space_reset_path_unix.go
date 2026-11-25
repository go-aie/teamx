//go:build darwin || linux

package room

import (
	"os"
	"syscall"
)

func resetIdentity(info os.FileInfo) (resetFileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return resetFileIdentity{}, ErrInvalid
	}
	return resetFileIdentity{uint64(stat.Dev), uint64(stat.Ino)}, nil
}
