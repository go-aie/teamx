//go:build !linux && !darwin

package srt

import (
	"errors"
	"os"
)

func hostCheck() error                             { return errors.New("SRT execution requires Linux") }
func multipleLinks(os.FileInfo) bool               { return true }
func checkWorkspaceMounts(string) error            { return errors.New("unsupported workspace mount observer") }
func checkProtectedPath(string, os.FileInfo) error { return errors.New("SRT execution requires Linux") }
