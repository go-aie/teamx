// Package gitlab provides TeamX built-in GitLab platforms, notifications and
// Workspace Git preparation independently of agent tools.
package gitlab

import (
	"embed"
)

// Only runtime resources are distributed. Existing room copies stay frozen.
//
//go:embed tools/git-askpass.sh tools/USAGE.md tools/git-only/USAGE.md
var resources embed.FS
