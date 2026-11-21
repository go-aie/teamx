// Package scion assembles the existing scion capabilities without starting resources.
package scion

import (
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Definition declares static capabilities; it does not access accounts or start queries.
func Definition() platforms.Definition {
	return platforms.Definition{
		Type:       platform.TypeDefinition{Provider: "scion", ToolType: "scion"},
		Connection: &platforms.Connection{Normalize: registry.Normalize, ValidProfile: func(_ string, p platform.CLIProfile) bool { return scion.ValidProfile(p) }},
		Tool: &platforms.Tool{Executable: "teamx-scion", OverrideEnvironment: "TEAMX_SCION_BIN", Definition: tool.Definition{
			Name: "scion", Description: "Scion native repository and Review CLI; uses account permissions", Version: "scn 0.1.13", Provider: "scion", Skill: "skills/scion/SKILL.md",
			Environment: map[string]string{"SCION_ENDPOINT": "instance", "SCION_HUB": "instance", "SCION_CREDENTIALS_FILE": "credentials_file", "SCION_CREDENTIALS": "credential_mode", "SCION_NO_DAEMON": "no_daemon", "SCION_CONFIG_FILE": "config_file"},
		}},
	}
}
