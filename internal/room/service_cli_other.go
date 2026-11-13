//go:build !darwin && !linux

package room

import (
	"errors"
	"io"
)

func runService(_ []string, _, _ io.Writer) error {
	return errors.New("service deployment requires Linux or macOS")
}
