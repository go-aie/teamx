//go:build darwin

package srt

import (
	"errors"
	"os"
	"syscall"
)

func hostCheck() error                  { return errors.New("SRT execution requires Linux") }
func checkWorkspaceMounts(string) error { return nil } // policy fixtures only; Start refuses this host
func multipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink > 1
}

func checkProtectedPath(string, os.FileInfo) error { return errors.New("SRT execution requires Linux") }
