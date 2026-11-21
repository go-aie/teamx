//go:build !darwin && !linux

package room

import "os"

func openWorkspaceFile(root *os.Root, name string) (*os.File, error) {
	return nil, fileError("workspace_source_unavailable")
}
