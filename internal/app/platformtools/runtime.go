// Package platformtools binds bounded application calls independently of HTTP.
// It owns no bindings, credentials, action records or call registry.
package platformtools

import (
	"context"
	"encoding/json"

	"github.com/go-aie/teamx/internal/platform"
)

type ReadRequest struct {
	ObjectRef string `json:"object_ref"`
	TargetRef string `json:"target_ref"`
	Scope     string `json:"scope"`
	Cursor    string `json:"cursor,omitempty"`
}

// Host is scoped to one authenticated invocation. Action bodies are decoded by
// the existing action owner, sharing the root CLI's strict DTOs and diagnostics.
// No server path, Store, manager or mutable registry crosses this boundary.
type Host interface {
	Admit(context.Context, bool) (context.Context, func(), error)
	Read(context.Context, ReadRequest) (json.RawMessage, error)
	Resolve(context.Context, json.RawMessage) (json.RawMessage, error)
	Execute(context.Context, json.RawMessage) (json.RawMessage, error)
	ActionHelp(entry, operation string) string
}

type Runtime interface {
	Help([]string) bool
	Run(context.Context, string, []string) ([]byte, error)
}
type Bind func(Host) Runtime

// Prepared keeps cancellation and settlement on the original admitted Call.
// Close is invoked after execution AND transport output have finished.
type Prepared struct {
	Context context.Context
	Close   func()
	Run     func() ([]byte, error)
}

func Prepare(ctx context.Context, entry string, args []string, bind Bind, host Host) (Prepared, error) {
	runtime := bind(host)
	ctx, close, err := host.Admit(ctx, runtime.Help(args))
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Context: ctx, Close: close, Run: func() ([]byte, error) { return runtime.Run(ctx, entry, args) }}, nil
}

// Diagnostic is authored application input/readiness feedback. The transport
// maps it to its existing request/conflict sentinels without echoing input values.
type Diagnostic struct {
	Field, Message, Hint string
	Conflict             bool
	Cause                error
}

func (e *Diagnostic) Error() string { return e.Field + ": " + e.Message }
func (e *Diagnostic) Unwrap() error { return e.Cause }

// ImageRef identifies an existing published Room image, never a path or URL.
type ImageRef struct {
	MessageSeq   uint64 `json:"message_seq"`
	AttachmentID string `json:"attachment_id"`
}
type Image struct {
	Data         []byte
	MIME, SHA256 string
}

// ImageLease retains publication/object pins and exact execution identity
// until Close, even after cancellation. Bytes returns ordered, read-only material.
type ImageLease interface {
	Bytes() []Image
	Check(context.Context) error
	Close()
}

// DispatchCheck is installed only by the action owner. Adapters must preserve
// it when merging cancellation/deadlines with their admitted execution context.
type DispatchCheckKey struct{}
type DispatchCheck func(context.Context, platform.TargetActionRequest, string) error

// ActionSession carries the original action owner's guarded runtime. Framework
// shutdown cannot stand in for a persisted result or business settlement.
type ActionSession struct {
	CapabilityBasis *platform.CapabilityEvidence
	Access          platform.AccessContext
	Capabilities    platform.ReferenceCapabilities
	Check           func(context.Context) error
	// CommitCheck runs under the owner admission lock before saving a target.
	CommitCheck func(context.Context) error
	Close       func()
}
