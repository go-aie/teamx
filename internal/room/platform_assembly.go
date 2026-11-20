package room

import (
	"context"
	"os"
	"sync"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Lazy immutable default for standalone Store/Room and offline commands. Managed
// consumers receive this same checked snapshot at construction; this is not a
// mutable registration mechanism. Provider selection lives only in Builtins.
var defaultPlatformCatalog = sync.OnceValue(func() *platforms.Catalog {
	catalog, err := platforms.Builtins()
	if err != nil {
		panic(err)
	}
	return catalog
})

func defaultPlatformTools() []tool.Definition {
	return defaultPlatformCatalog().Tools(func(name, override string) string {
		return tool.ResolveExecutable(name, os.Getenv(override))
	})
}

// Profile verification needs the service directory; static assembly does not.
func (m *roomManager) verifyScionProfile(ctx context.Context, origin string, p platform.CLIProfile) platform.Validation {
	return scion.VerifyProfile(ctx, m.store.dir, origin, p)
}
