// Package agent defines the operations used by the selected member backend.
// Factories and constructors acquire no resources until OpenSession is called.
package agent

import (
	"context"
	"errors"
	"time"

	"github.com/go-aie/teamx/internal/identity"
)

type RoomIdentity = identity.RoomIdentity
type MemberIdentity = identity.MemberIdentity

// Input is a wake snapshot, never a message body or a consumption receipt.
type Input struct {
	MemberIdentity
	Inbox           *InboxInput `json:"inbox,omitempty"`
	Revision        uint64      `json:"revision"`
	LatestSeq       uint64      `json:"latest_seq"`
	NewestUnreadSeq uint64      `json:"newest_unread_seq"`
	UnreadCount     int         `json:"unread_count"`
}

type InboxInput struct {
	Recovery    bool   `json:"recovery,omitempty"`
	Through     uint64 `json:"through"`
	SnapshotSeq uint64 `json:"snapshot_seq"`
}

// Launch contains only the effective, per-session execution binding. It is
// ephemeral: credentials, tokens and MCP headers must never be persisted.
type Launch struct {
	BinaryPath, Workspace, StateDir, Instructions string
	BinaryArgs                                    []string
	Artifact                                      Artifact
	Model                                         ModelConfig
	Credential                                    *Credential `json:"-"`
	ToolEnv                                       map[string]string
	ExecutionToken                                string          `json:"-"`
	MCPServers                                    []MCPHTTPServer `json:"-"`
	CapabilityDigest                              string
	InputBridge                                   bool
	ObserveActivity                               func() `json:"-"`
	TurnTimeout                                   time.Duration
	TextBytes                                     int
}

type Credential struct {
	Ref   string `json:"-"`
	Value string `json:"-"`
}
type MCPHTTPServer struct {
	Type    string      `json:"type"`
	Name    string      `json:"name"`
	URL     string      `json:"url"`
	Headers []MCPHeader `json:"headers"`
}
type MCPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type RuntimeIdentity struct {
	BinaryPath   string   `json:"binary_path"`
	BinarySHA256 string   `json:"binary_sha256"`
	BinaryArgs   []string `json:"binary_args,omitempty"`
	ScriptSHA256 []string `json:"script_sha256,omitempty"`
	Version      string   `json:"version"`
	Agent        Artifact `json:"agent,omitzero"`
}

type Component interface {
	RunPrompt(context.Context, []ContentBlock) (TurnResult, error)
	SupportsImages() bool
	Close(context.Context) error
}

type Reference struct {
	Version int `json:"version"`
	MemberIdentity
	Directory    string `json:"directory"`
	Pending      bool   `json:"pending,omitempty"`
	ManagedModel bool   `json:"managed_model,omitempty"`
}

// Checkpoints stays with the directory/Store owner. Implementations validate
// the caller identity and retain the original lock and snapshot writer.
type Checkpoints interface {
	LoadReference() (Reference, error)
	StateExists(directory string) (bool, error)
}

// Factory is the one selection used by live members and saved-session checks.
// Reference and check operations must not launch execution or change data.
type Factory interface {
	OpenSession(context.Context, Host, bool) (*Session, error)
	Open(context.Context, Launch, bool) (Component, error)
	Reference(Checkpoints, MemberIdentity, string) (Reference, bool, error)
	CheckResume(string) error
	CheckReset(string) error
	CheckResumeConfig(Launch) error
	InspectRuntime(context.Context, Launch) (RuntimeIdentity, error)
}

// Host supplies one complete session's application operations. Input and
// material preparation, proof confirmation and revocation retain their owners.
// No service, Store, actor address or general resource lookup crosses this API.
type Host interface {
	PrepareOpen(context.Context) (Launch, error)
	Opened(*Artifact) error
	PrepareTurn(context.Context, Input, TurnCapabilities) (*PreparedTurn, error)
	Release()
}

// TurnCapabilities supplies only the features needed for input preparation.
type TurnCapabilities struct {
	Images             bool
	AddRedactionSecret func(string)
}

// PreparedTurn retains its material/input leases until Finish has recorded the
// actual prompt outcome and Close has joined any revocation work.
type PreparedTurn struct {
	Prompt []ContentBlock
	Finish func(TurnResult, error) error
	Close  func()
}

type Session struct {
	component Component
	host      Host
}

// OpenSession preserves ownership of a partially opened component on failure.
// The caller must close a non-nil Session even when err is non-nil.
func OpenSession(ctx context.Context, factory Factory, host Host, resume bool) (*Session, error) {
	launch, err := host.PrepareOpen(ctx)
	if err != nil {
		host.Release()
		return nil, err
	}
	component, err := factory.Open(ctx, launch, resume)
	if component == nil {
		host.Release()
		if err == nil {
			err = errors.New("agent factory returned no session")
		}
		return nil, err
	}
	if err == nil {
		var artifact *Artifact
		if runtime, ok := component.(interface{ RuntimeAgent() Artifact }); ok {
			value := runtime.RuntimeAgent()
			artifact = &value
		}
		err = host.Opened(artifact)
	}
	return &Session{component: component, host: host}, err
}

func (s *Session) Run(ctx context.Context, input Input) error {
	turn, err := s.host.PrepareTurn(ctx, input, capabilities(s.component))
	if err != nil {
		return err
	}
	if turn.Close != nil {
		defer turn.Close()
	}
	result, err := s.component.RunPrompt(ctx, turn.Prompt)
	return turn.Finish(result, err)
}

func (s *Session) Close(ctx context.Context) error {
	s.host.Release()
	return s.component.Close(ctx)
}

func (s *Session) Activity() (TurnResult, bool) {
	if reader, ok := s.component.(interface{ Activity() (TurnResult, bool) }); ok {
		return reader.Activity()
	}
	return TurnResult{}, false
}

func capabilities(component Component) TurnCapabilities {
	result := TurnCapabilities{Images: component.SupportsImages()}
	if private, ok := component.(interface{ AddRedactionSecret(string) }); ok {
		result.AddRedactionSecret = private.AddRedactionSecret
	}
	return result
}
