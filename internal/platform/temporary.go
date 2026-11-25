package platform

import (
	"errors"
	"os"
	"path/filepath"
)

// PrivateTemp creates a call directory under a deployment-bound Space root.
// Empty roots preserve the personal/native CLI behavior. Callers still remove
// their directory normally; a crashed call remains owned by that Space.
func PrivateTemp(root, pattern string) (string, error) {
	if root != "" {
		real, err := filepath.EvalSymlinks(root)
		if err != nil || !filepath.IsAbs(root) || real != root {
			return "", errors.New("invalid private temporary root")
		}
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("invalid private temporary root")
		}
	}
	return os.MkdirTemp(root, pattern)
}
