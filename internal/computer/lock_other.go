//go:build !unix

package computer

import "fmt"

func lockServiceDirectory(string) (func() error, error) {
	return nil, fmt.Errorf("Computer service requires Unix file locks")
}
