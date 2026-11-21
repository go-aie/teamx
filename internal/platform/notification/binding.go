package notification

import (
	"context"
	"fmt"

	"github.com/go-aie/teamx/internal/platform"
)

// Account is an immutable admission snapshot. It contains credential references,
// never tokens. The host restricts lookup to the selected source's provider.
type Account struct {
	PlatformID, Generation             string
	Identity                           platform.AccountIdentity
	CLIProfile                         *platform.CLIProfile
	PlatformVersion, CredentialVersion uint64
	CredentialFingerprint              string
}

func (Account) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED notification account]") }

// RuntimeHost exposes the existing connection and subscription owners. These
// methods are only valid synchronously inside Admit or CheckLoaded, under the
// host's admission gate. They must not be retained by runners or called by Bind.
// Returned snapshots are detached; this interface offers no writes or delivery.
type RuntimeHost interface {
	Account(platform.RoomIdentity, string) (Account, error)
	States(platform.RoomIdentity) []State
}

// Runtime binds a source to its admission and recovery checks. The common host
// still owns Room/member gates, query revision checks, durable delivery and drain.
// Admit and CheckLoaded run under the host admission gate and must not block.
type Runtime struct {
	Source      Source
	Admit       func(context.Context, State) (Scope, error)
	CheckLoaded func(platform.RoomIdentity) error
}

// Bind performs no I/O or resource acquisition. Only NewRunner/Step may start
// collection, after normal startup has loaded and checked all subscriptions.
type Bind func(Source, RuntimeHost) (Runtime, error)
