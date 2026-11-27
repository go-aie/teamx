package room

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// platformProgram selects service dependencies without changing connection
// evidence. Each invocation retains the selected paths until settlement.
func (m *roomManager) platformProgram(provider string, original *platform.CLIProfile) (string, error) {
	switch provider {
	case "gitlab":
		return tool.ResolveExecutableIn("teamx-gitlab", environmentValue(m.config.environment, "TEAMX_GITLAB_BIN"), m.config.platformBinDir, m.config.environment)
	case "scion":
		if m.config.hostTools.restricted {
			return "", hostToolUnavailable("external_tools")
		}
		if m.config.platformBinDir == "" && original != nil {
			return tool.ResolveExecutableIn("scn", original.Executable, "", m.config.environment)
		}
		override := environmentValue(m.config.environment, "TEAMX_SCION_BIN")
		selected, err := tool.ResolveExecutableIn("teamx-scion", override, m.config.platformBinDir, m.config.environment)
		if err != nil && override == "" && m.config.platformBinDir == "" {
			return tool.ResolveExecutableIn("scn", environmentValue(m.config.environment, "TEAMX_SCN_BIN"), "", m.config.environment)
		}
		return selected, err
	case "lark":
	default:
		return "", ErrRequest
	}
	if m.config.platformBinDir == "" && !m.config.hostTools.restricted && original != nil {
		return original.Executable, nil
	}
	return tool.ResolveExecutableIn("lark-cli", environmentValue(m.config.environment, "TEAMX_LARK_BIN"), m.config.platformBinDir, m.config.environment)
}

// Project the same native dependency into both the local preflight and the
// wrapper's filtered process environment. The admitted Definition owns these
// values until its process settles; no persistent account evidence is changed.
func (m *roomManager) bindProgramDependencies(d tool.Definition) tool.Definition {
	environment := append([]string{}, m.config.environment...)
	for _, dep := range d.Dependencies {
		selected, err := tool.ResolveDependencyIn(dep.Name, environmentValue(m.config.environment, dep.OverrideEnvironment), d.Executable, m.config.platformBinDir != "", m.config.environment)
		if err != nil || d.Executable == "" {
			// An invalid selection must remain unavailable, including when a
			// different, working native binary exists on the service PATH.
			d.Executable = ""
			return d
		}
		filtered := environment[:0]
		for _, entry := range environment {
			if !strings.HasPrefix(entry, dep.OverrideEnvironment+"=") {
				filtered = append(filtered, entry)
			}
		}
		environment = append(filtered, dep.OverrideEnvironment+"="+selected)
	}
	d.ProcessEnvironment = environment
	return d
}

func (m *roomManager) configureLarkRuntime(home string) error {
	var err error
	if m.config.platformBinDir != "" || m.config.hostTools.restricted {
		executable, selectErr := m.platformProgram("lark", nil)
		if selectErr != nil {
			return fmt.Errorf("Lark CLI dependency unavailable: %w", selectErr)
		}
		m.larkClients, err = m.larkClients.WithExecutable(executable)
		if err != nil {
			return err
		}
	}
	if m.config.hostTools.restricted {
		if err := checkWorkspaceDirectory(home, "platform-tmp"); err != nil {
			return err
		}
		m.config.platformTempRoot = filepath.Join(home, "platform-tmp")
		if err := makeDurableDir(m.config.platformTempRoot); err != nil {
			return err
		}
		m.larkClients, err = m.larkClients.WithTemporaryRoot(m.config.platformTempRoot)
		if err != nil {
			return err
		}
		// Same platform implementation; only its CLI storage/executable binding
		// changes. Legacy personal profiles remain on their original backend.
		if err := checkWorkspaceDirectory(home, "platform-cli/lark"); err != nil {
			return err
		}
		executable := m.larkClients.Executable(platform.CLIProfile{})
		m.larkClients, err = m.larkClients.WithStorage(filepath.Join(home, "platform-cli", "lark"), executable)
		if err != nil {
			return err
		}
	}
	return nil
}

// scionProgram projects one selection into the existing process description.
// External native scn needs no wrapper dependency. Installed services declare a
// wrapper/native pair, including when an administrator renames the wrapper.
func (m *roomManager) scionProgram(original *platform.CLIProfile) (tool.Definition, error) {
	executable, err := m.platformProgram("scion", original)
	d := scion.Definition(executable)
	if err != nil {
		return d, err
	}
	d.ProcessEnvironment = m.config.environment
	requested := environmentValue(m.config.environment, "TEAMX_SCION_BIN")
	if m.config.platformBinDir == "" && original != nil {
		requested = original.Executable
	}
	native := filepath.Base(requested) == "scn" || filepath.Base(executable) == "scn"
	if !native && (m.config.platformBinDir != "" || filepath.Base(requested) == "teamx-scion" || filepath.Base(executable) == "teamx-scion") {
		d.Dependencies = []tool.ExecutableDependency{{Name: "scn", OverrideEnvironment: "TEAMX_SCN_BIN"}}
		d = m.bindProgramDependencies(d)
	}
	return d, nil
}
