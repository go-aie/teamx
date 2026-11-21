// Package notification declares notification sources and their runtime bindings.
// Sources keep their native state and codec. TeamX owns admission, persistence,
// revision checks, Inbox receipts, cancellation and shutdown settlement.
package notification

import internal "github.com/go-aie/teamx/internal/platform/notification"

type SourceID = internal.SourceID
type Key = internal.Key
type Metadata = internal.Metadata
type Scope = internal.Scope
type State = internal.State
type Source = internal.Source
type Runner = internal.Runner
type StepHost = internal.StepHost
type Limits = internal.Limits
type Runtime = internal.Runtime
type RuntimeHost = internal.RuntimeHost
type Account = internal.Account
type Bind = internal.Bind
type Target = internal.Target
type Route = internal.Route
type Item = internal.Item
type Content = internal.Content
type Participant = internal.Participant
type Context = internal.Context
type Attachment = internal.Attachment
type View = internal.View

// Declaration constructs a zero-I/O source and binds its runtime snapshot host.
// Bind is required for external sources; Compose never calls it.
type Declaration struct {
	ID   SourceID
	New  func() Source
	Bind Bind
}

// Provider contributes notification channels without requiring a tool or account.
// Input is optional at registration, but durable Inbox input requires a codec.
type Provider struct {
	Provider string
	Sources  []Declaration
	Input    *InputCapability
	Project  func(Item) View
}

var (
	ErrRequest  = internal.ErrRequest
	ErrInvalid  = internal.ErrInvalid
	ErrIdentity = internal.ErrIdentity
	ErrConflict = internal.ErrConflict
	ErrBusy     = internal.ErrBusy
	ErrClosing  = internal.ErrClosing
	ErrCapacity = internal.ErrCapacity
)

// DeliveryID stays stable across retries and process restarts.
func DeliveryID(subscription Key, itemKey string) string {
	return internal.DeliveryID(subscription, itemKey)
}

func Project(item Item) View { return internal.Project(item) }
