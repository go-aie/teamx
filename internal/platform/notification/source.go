// Package notification owns subscription execution, persistence and delivery.
// It does not import providers or Room. All catalog operations use the owner's
// single Gate; callbacks below run with that gate held. Source queries run free
// of it and can only publish candidates through a checked StepHost.
package notification

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

var (
	ErrRequest  = errors.New("invalid notification request")
	ErrInvalid  = errors.New("invalid notification snapshot")
	ErrIdentity = errors.New("notification identity mismatch")
	ErrConflict = errors.New("notification scope changed")
	ErrBusy     = errors.New("notification scope busy")
	ErrClosing  = errors.New("notification service closing")
	ErrCapacity = errors.New("notification capacity exceeded")
)

type SourceID struct {
	Provider string `json:"provider"`
	Channel  string `json:"channel"`
}
type Key struct {
	Owner          platform.RoomIdentity
	Source         SourceID
	SubscriptionID string
}
type Metadata struct {
	Key               Key
	Version           uint64
	Enabled           bool
	Retired           bool
	PlatformID        string
	Observed, Pending int
	DeliveryError     string
}

// Scope carries a provider-decoded, immutable query binding and separate stamps.
// CredentialStamp excludes metadata edits; Revision includes all connection edits.
// Providers validate Data using a concrete schema before executing a request.
type Scope struct {
	Data                      []byte
	Revision, CredentialStamp string
	Directory                 string
	Current                   bool
}
type Item struct {
	Route      Route              `json:"route,omitzero"`
	Key        string             `json:"key"`
	DeliveryID string             `json:"delivery_id"`
	CreatedAt  time.Time          `json:"created_at"`
	Order      string             `json:"order"` // Provider-defined stable tie break; never interpreted as an ID.
	Text       string             `json:"text"`
	Source     platform.SourceRef `json:"source"`
	Content    *Content           `json:"content,omitempty"`
}

// State is a provider's versioned, concrete progress record. The source codec
// validates its entire schema. The core stores it opaquely and atomically; it
// never combines unrelated cursor fields or decodes an unvalidated map.
type State interface {
	Metadata() Metadata
	Validate(platform.RoomIdentity) error
	CloneState() State
	PendingItems() []Item
	Notification(string) (Item, bool)
	Acknowledge(string) State
	WithDeliveryError(string) State
	InvalidateCredential() (State, error)
	ScopeFailed(error) State
	QueryPaused() bool
}
type Source interface {
	ID() SourceID
	Schema() int
	Decode([]byte, platform.RoomIdentity) (State, error)
	NewRunner(StepHost) Runner
}
type Runner interface {
	Step(context.Context) (time.Duration, bool)
}

// A Step performs at most one remote query. A source may emit a local checkpoint
// before that query and a result candidate after it. Commit acknowledges each
// candidate synchronously; a failure must not advance the source's runtime cache.
// Read and Commit acquire the owner's Gate internally, never a second mutex.
type StepHost interface {
	Read(context.Context) (State, Scope, error)
	Commit(State) error
	Now() time.Time
	Jitter(time.Duration) time.Duration
	Limits() Limits
	ConsumeForce() bool
}

type Limits struct {
	Observed, Inbox int
	Bytes           int64
	SlicePages      int
	SliceBytes      int64
	SliceTime       time.Duration
}

func DefaultLimits() Limits { return Limits{100000, 10000, 64 << 20, 20, 16 << 20, 30 * time.Second} }

// Host is the Room-owned admission and final-commit boundary. The service has no
// private mutex to hold while calling it. Gate -> Room service read lock -> actor
// is the only lock order; no callback reacquires Gate.
type Host struct {
	Gate            sync.Locker
	Check           func(writing bool) error
	Directory       func(platform.RoomIdentity) (string, error)
	EnsureDirectory func(string) error
	Save            func(string, any, int64) error
	Admit           func(context.Context, State) (Scope, error)
	Deliver         func(context.Context, Metadata, Item) error
}

// QueryContext applies the common hard bound even when a source requests a
// longer budget. Sources can further shorten it for a page or scan slice.
func QueryContext(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, max(time.Millisecond, min(30*time.Second, budget)))
}
