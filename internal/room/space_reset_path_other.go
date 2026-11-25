//go:build !darwin && !linux

package room

import "os"

func resetIdentity(os.FileInfo) (resetFileIdentity, error) { return resetFileIdentity{}, ErrInvalid }
