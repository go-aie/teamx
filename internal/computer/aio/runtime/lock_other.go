//go:build !unix

package runtime

import "fmt"

func lockDirectory(string) (func() error, error) {
	return nil, fmt.Errorf("Computer Runtime requires Unix file locks")
}
