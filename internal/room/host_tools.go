package room

import (
	"fmt"
	"strings"

	"github.com/go-aie/teamx/internal/app/platforms"
)

// Selected once by deployment assembly, never decoded from Space settings or
// accepted from a request. Only bounded GitLab API and built-in Lark tools are
// admitted; repository execution and Scion require separate acceptance.
type hostToolPolicy struct{ restricted bool }

func (p hostToolPolicy) scionAvailable(catalog *platforms.Catalog) bool {
	if p.restricted || catalog == nil {
		return false
	}
	_, ok := catalog.Types().LookupType("scion")
	return ok
}

func (p hostToolPolicy) validateCatalog(catalog *platforms.Catalog) error {
	if !p.restricted {
		return nil
	}
	for _, provider := range []string{"gitlab", "lark"} {
		if catalog == nil {
			return fmt.Errorf("required platform implementation missing: %s", provider)
		}
		if _, ok := catalog.Types().LookupType(provider); !ok {
			return fmt.Errorf("required platform implementation missing: %s", provider)
		}
	}
	return nil
}

func deploymentHostTools(mode string) (hostToolPolicy, error) {
	switch mode {
	case "", "personal":
		return hostToolPolicy{}, nil
	case "multi-user":
		return hostToolPolicy{restricted: true}, nil
	default:
		return hostToolPolicy{}, fmt.Errorf("unsupported deployment mode")
	}
}

func (p hostToolPolicy) catalog(original *platforms.Catalog) *platforms.Catalog {
	if !p.restricted {
		return original
	}
	factories := []platforms.Factory{}
	if original != nil {
		for _, definition := range original.Definitions() {
			if definition.Type.Provider == "gitlab" || definition.Type.Provider == "lark" {
				factories = append(factories, func() platforms.Definition { return definition })
			}
		}
	}
	catalog, err := platforms.NewCatalog(factories...)
	if err != nil {
		panic(err) // immutable subset of an already checked catalog
	}
	return catalog
}

func (p hostToolPolicy) checkRepository(w *workspaceConfig) error {
	if p.restricted && w != nil && (w.Repository != nil || w.Binding != nil) {
		return hostToolUnavailable("managed_repository")
	}
	return nil
}

func hostToolUnavailable(capability string) error {
	return invalidField("capability", capability+" is not available in this deployment")
}

// Defense for old clients and routes that predate catalog contributions. Local
// input, matters, calendar, files and model operations keep their own owners.
func (p hostToolPolicy) checkHTTP(path string) error {
	if !p.restricted {
		return nil
	}
	if strings.HasPrefix(path, "/v1/platforms/scion/") {
		return hostToolUnavailable("scion")
	}
	if path == "/v1/members/computer" || path == "/v1/computer" || strings.HasPrefix(path, "/v1/computer/") {
		return hostToolUnavailable("computer")
	}
	return nil
}

const sharedToolsContract = "shared-api"

func (a *serverAccess) unavailableSpacePlatform(provider string) bool {
	return a.control != nil && a.space.SpaceID != a.control.space.SpaceID && (provider == "scion" && !a.privateScion || provider == "lark" && !a.privateLark)
}

const sharedGitLabSkill = `# GitLab

Use the exact bound ENTRY and its selected account. The existing CLI supports
tools run ENTRY api ENDPOINT --method METHOD [-f key=value | -F key=value]
[--input -] [--paginate], plus work-items and issue discussions extensions.
Use explicit project IDs/paths; read tools run ENTRY --help for current flags.
Files must be supplied through stdin. Repository commands, host overrides and
repository placeholders are unavailable. For managed comments/replies, use
TeamX actions resolve/execute/status and their existing authorization/receipt
workflow (actions --help); preserve the original action ID after interruption.
Never switch connections or retry an unconfirmed write without checking it.
`

func (p hostToolPolicy) resourceLimits(l executionResourceLimits) executionResourceLimits {
	if p.restricted {
		for _, entry := range []struct {
			value    *int
			fallback int
		}{
			{&l.Spaces, 64}, {&l.ActiveSpaces, 16}, {&l.SpacesPerUser, 16}, {&l.ActiveSpacesPerUser, 8},
			{&l.Sessions, 32}, {&l.SessionsPerUser, 8}, {&l.SessionsPerSpace, 4},
		} {
			if *entry.value == 0 {
				*entry.value = entry.fallback
			}
		}
	}
	return l
}
