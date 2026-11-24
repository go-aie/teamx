package agent

import (
	"context"
	"os"
	"time"
)

// ExecutionBinding is supplied by the owning host, never by model output. The
// generation is the original private Session directory, including on resume.
type ExecutionBinding struct {
	UserID, SpaceID string
	MemberIdentity
	Generation string
	Probe      bool
}

// ExecutionIdentity deliberately separates implementation bytes from the
// effective per-generation policy. Neither field includes transport secrets.
type ExecutionIdentity struct {
	Kind          string `json:"kind"`
	Version       string `json:"version"`
	RuntimeSHA256 string `json:"runtime_sha256"`
	PolicySHA256  string `json:"policy_sha256,omitempty"`
}

// ExecutionRequest is an immutable process acquisition, independent of ACP.
// Files are explicit private control channels, never additional inherited fds.
type ExecutionRequest struct {
	BinaryPath, Workspace, StateDir string
	Args, Environment               []string
	Binding                         ExecutionBinding
	Files                           []*os.File
	EnvironmentKeys                 []string // exact credential variable names authorized for this acquisition
	Endpoints                       []string // exact model and authenticated callback URLs, without headers
	Supervised                      bool     // seal steady supervisor/bridge processes at Ready
	RelayExecutables                []string // pinned fork-per-connection relays; all children remain owned until Stop
}

// ExecutionFactory is the small execution seam used by the DSH protocol
// adapter. Identify is read-only and must not start resources. Start may return
// a non-nil handle with an error; the caller must retain it until Stop finishes.
type ExecutionFactory interface {
	Identify(ExecutionRequest) (ExecutionIdentity, error)
	Start(context.Context, ExecutionRequest, func(error)) (Execution, error)
}

// EndpointRouter supplies a stable transport spelling for an already approved
// endpoint. It grants no new destination or authority and performs no IO.
type EndpointRouter interface {
	RouteEndpoint(string) (string, error)
}

// Execution owns channels and every descendant/proxy until Stop returns.
// A caller timeout bounds its wait, never this ownership. Ready seals only
// infrastructure created before the first prompt; tools must settle separately.
type Execution interface {
	Stdin() *os.File
	Stdout() *os.File
	Ready() error
	Check() error
	WaitToolsStopped(context.Context) error
	Stop(time.Duration)
	Diagnostic() (string, bool) // bounded stderr; the protocol owner redacts secrets
	Result() error              // only after Stop; abnormal/forced exit prevents normal resume
}
