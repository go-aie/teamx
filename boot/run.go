// Package boot runs the ordinary TeamX CLI with an explicit capability selection.
package boot

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/go-aie/teamx/extension"
	"github.com/go-aie/teamx/internal/app/extensions"
	"github.com/go-aie/teamx/internal/room"
)

// ExitError preserves a controlled tool's exit status. The executable owns os.Exit.
type ExitError = room.ExitError

// Run accepts the result of extension.Compose, including an explicit empty
// selection. It never substitutes the built-in complete selection.
func Run(selected *extension.Composition, args []string, stdout, stderr io.Writer) error {
	catalog, err := extensions.Catalog(selected)
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "extensions" {
		if len(args) != 2 || args[1] != "check" {
			return fmt.Errorf("usage: teamx extensions check")
		}
		// This resolver returns declaration text without executable discovery.
		declared := catalog.Tools(func(name, _ string) string { return name })
		tools := make([]toolSummary, 0, len(declared))
		for _, t := range declared {
			tools = append(tools, toolSummary{t.Name, t.Description, t.Version, t.Provider})
		}
		return json.NewEncoder(stdout).Encode(struct {
			Extensions    []string              `json:"extensions"`
			Tools         []toolSummary         `json:"tools"`
			Notifications []notificationSummary `json:"notifications"`
		}{extensions.Names(selected), tools, notificationSummaries(catalog)})
	}
	return room.RunWithCatalog(args, stdout, stderr, catalog)
}

type toolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Provider    string `json:"provider,omitempty"`
}
