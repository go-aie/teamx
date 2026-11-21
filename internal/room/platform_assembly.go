package room

import (
	"context"
	"sync"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
)

// Unconfigured internal consumers have no selected capabilities. The executable
// supplies its immutable catalog explicitly; no service imports the standard selection.
var emptyPlatformCatalog = sync.OnceValue(func() *platforms.Catalog {
	catalog, err := platforms.NewCatalog()
	if err != nil {
		panic(err)
	}
	return catalog
})

// Profile verification needs the service directory; static assembly does not.
func (m *roomManager) verifyScionProfile(ctx context.Context, origin string, p platform.CLIProfile) platform.Validation {
	return scion.VerifyProfile(ctx, m.store.dir, origin, p)
}
