// Package computer selects the built-in Computer implementation. Registration
// describes a capability; no connection or remote probe happens at init time.
package computer

import (
	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
)

func init() {
	computer.Register("aio", func(endpoint, credential string) (computer.ManagedProvider, error) {
		return aio.New(endpoint, credential)
	})
}
