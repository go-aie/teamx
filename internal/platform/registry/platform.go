package registry

import (
	"strings"

	"github.com/go-aie/teamx/internal/platform"
)

// Entry identity is allocated once, independent of the display name, account,
// and caller-supplied platform ID. Recreating a deleted ID gets a fresh entry.
func newToolEntry(provider string) (string, error) {
	d, ok := platform.LookupType(provider)
	if !ok || d.ToolType == "" {
		return "", nil
	}
	id, err := newOpaqueID()
	if err != nil {
		return "", err
	}
	return d.ToolType + "-" + id, nil
}

func validToolEntry(c platform.Platform) bool {
	d, ok := platform.LookupType(c.Provider)
	if !ok || d.ToolType == "" {
		return c.ToolEntryID == ""
	}
	return strings.HasPrefix(c.ToolEntryID, d.ToolType+"-") && validRestartID(strings.TrimPrefix(c.ToolEntryID, d.ToolType+"-"))
}
