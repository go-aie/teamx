// Package computer defines provider-independent managed Computer resources.
// It has no dependency on Room, Agent scheduling, or a particular provider.
package computer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-aie/teamx/internal/identity"
)

const (
	RequestLimit  = 128 << 10
	ResponseLimit = 8 << 20
)

type Code string

const (
	Disabled         Code = "computer_disabled"
	Unavailable      Code = "computer_unavailable"
	Capacity         Code = "screen_capacity"
	Busy             Code = "screen_busy"
	HumanControl     Code = "human_control"
	Stale            Code = "stale_screen"
	Unknown          Code = "operation_unknown"
	ContractMismatch Code = "contract_mismatch"
	Invalid          Code = "invalid_request"
)

type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// AdmissionRefusal proves a local gate rejected a call before provider IO.
// Transport failures with the same public code carry no such proof.
type AdmissionRefusal struct{ Cause *Error }

func (e *AdmissionRefusal) Error() string    { return e.Cause.Error() }
func (e *AdmissionRefusal) Unwrap() error    { return e.Cause }
func Refuse(code Code, message string) error { return &AdmissionRefusal{&Error{code, message}} }

func (e *Error) Error() string             { return string(e.Code) + ": " + e.Message }
func Fail(code Code, message string) error { return &Error{code, message} }
func Is(err error, code Code) bool         { var e *Error; return errors.As(err, &e) && e.Code == code }

// Owner and Member together identify persistent ownership, never a display name.
type Owner struct {
	InstallationID string `json:"installation_id"`
	OwnerID        string `json:"owner_id"`
	ComputerID     string `json:"computer_id"`
}
type Member = identity.MemberIdentity

// AdmissionVersion orders connection and member preference changes independently.
type AdmissionVersion struct {
	Connection uint64 `json:"connection"`
	Member     uint64 `json:"member"`
}
type MemberAdmission struct {
	Binding Binding `json:"binding"`
	Enabled bool    `json:"enabled"`
	Retired bool    `json:"retired,omitempty"`
}

type Binding struct {
	Admission AdmissionVersion `json:"admission,omitzero"`
	Owner     Owner            `json:"owner"`
	Member    Member           `json:"member"`
	Service   string           `json:"service"`
	Lease     string           `json:"lease"`
}

func (b Binding) Validate() error {
	for _, v := range []string{b.Owner.InstallationID, b.Owner.OwnerID, b.Owner.ComputerID, b.Member.RoomID, b.Member.RoomInstance, b.Member.AgentID, b.Member.MembershipID, b.Service, b.Lease} {
		if strings.TrimSpace(v) == "" || len(v) > 256 {
			return Fail(Invalid, "complete bounded ownership and service lease required")
		}
	}
	return nil
}
func (b Binding) Key() string {
	return Hash(struct {
		Owner  Owner
		Member Member
	}{b.Owner, b.Member})
}
func (b Binding) Slot() string {
	return Hash(struct {
		Owner       Owner
		Room, Agent string
	}{b.Owner, b.Member.RoomID, b.Member.AgentID})
}

type Claim struct {
	Owner         Owner  `json:"owner"`
	Service       string `json:"service"`
	ExpectedLease string `json:"expected_lease"`
}

func (c Claim) Validate() error {
	for _, v := range []string{c.Owner.InstallationID, c.Owner.OwnerID, c.Owner.ComputerID, c.Service} {
		if strings.TrimSpace(v) == "" || len(v) > 256 {
			return Fail(Invalid, "complete bounded owner and service identity required")
		}
	}
	return nil
}

type Info struct {
	MemberAdmission bool     `json:"member_admission"`
	Owner           Owner    `json:"owner"`
	InstanceID      string   `json:"instance_id"`
	BootID          string   `json:"boot_id"`
	Lease           string   `json:"lease"`
	MaxScreens      int      `json:"max_screens"`
	Running         int      `json:"running"`
	Contract        Contract `json:"contract"`
}
type Scope struct {
	Session string `json:"session"`
	Turn    string `json:"turn,omitempty"`
}

func (s Scope) Matches(other Scope) bool {
	return (s.Session == "" || s.Session == other.Session) && (s.Turn == "" || s.Turn == other.Turn)
}

type Call struct {
	ID             string          `json:"id"`
	Scope          Scope           `json:"scope"`
	Tool           string          `json:"tool"`
	Arguments      json.RawMessage `json:"arguments"`
	ContractDigest string          `json:"contract_digest"`
}

func (c Call) Digest() string {
	return Hash(struct {
		Scope     Scope
		Tool      string
		Arguments json.RawMessage
		Contract  string
	}{c.Scope, c.Tool, c.Arguments, c.ContractDigest})
}

type ScreenInfo struct {
	ID      string  `json:"id"`
	Epoch   uint64  `json:"epoch"`
	Status  string  `json:"status"`
	Binding Binding `json:"binding"`
}
type Operation struct {
	ID      string `json:"id"`
	Digest  string `json:"digest"`
	Epoch   uint64 `json:"epoch"`
	Scope   Scope  `json:"scope"`
	Tool    string `json:"tool"`
	State   string `json:"state"` // pending, completed, unknown; never a website success receipt
	Settled bool   `json:"settled"`
}
type Outcome struct {
	Operation       Operation       `json:"operation"`
	Result          json.RawMessage `json:"result,omitempty"` // complete native MCP result, memory only
	ResultAvailable bool            `json:"result_available"`
}

// Claim is an explicit compare-and-swap handoff. A service generation that was
// fenced out cannot reclaim ownership, including after a Runtime restart.
type Provider interface {
	Inspect(context.Context) (Info, error)
	Claim(context.Context, Claim) (string, error)
	EnsureScreen(context.Context, Binding) (Screen, error)
}

// ManagedProvider adds inventory and restoration for connection settlement.
// Inventory never creates a desktop or changes its service ownership.
type ManagedProvider interface {
	Provider
	SetMemberAdmission(context.Context, MemberAdmission) error
	Screens(context.Context, Owner) ([]ScreenInfo, error)
	Restore(ScreenInfo) Screen
}
type Screen interface {
	Info() ScreenInfo
	Tools(context.Context) (Contract, error)
	Call(context.Context, Call) (Outcome, error)
	Operation(context.Context, string) (Outcome, error)
	Drain(context.Context, Scope) error
	Stop(context.Context) error // preserves profile and unknown business outcomes
}

// DisplayScreen uses the same managed screen for native calls and human input.
// View transports RFB bytes; providers enforce read-only access server-side.
type DisplayScreen interface {
	Screen
	Control(context.Context, ControlRequest) (ControlState, error)
	View(context.Context, ViewRequest) (io.ReadWriteCloser, error)
	ViewAsset(context.Context, string) ([]byte, error)
}
type ControlRequest struct {
	Context  string `json:"context"`
	Sequence uint64 `json:"sequence"`
	Action   string `json:"action"` // hold, activate, renew, release
	ID       string `json:"id"`
	Client   string `json:"client"`
}
type ControlState struct {
	Context  string `json:"context"`
	Sequence uint64 `json:"sequence"`
	ID       string `json:"id"`
	Client   string `json:"client"`
	Phase    string `json:"phase"`
}
type ViewRequest struct {
	ID     string `json:"id,omitempty"`
	Client string `json:"client,omitempty"`
}

// Tool keeps the entire native definition, including extensions. ReadOnly is
// independently audited policy; native annotations do not grant it.
type Tool struct {
	RequiresImages bool            `json:"requires_images,omitempty"`
	Name           string          `json:"name"`
	NativeName     string          `json:"native_name"`
	Definition     json.RawMessage `json:"definition"`
	ReadOnly       bool            `json:"read_only"`
}
type Contract struct {
	Provider string            `json:"provider"`
	Versions map[string]string `json:"versions"`
	Tools    []Tool            `json:"tools"`
	Digest   string            `json:"digest"`
}

func NewContract(provider string, versions map[string]string, tools []Tool) (Contract, error) {
	c := Contract{Provider: provider, Versions: versions, Tools: append([]Tool(nil), tools...)}
	if provider == "" || len(tools) == 0 || len(tools) > 128 {
		return c, Fail(ContractMismatch, "bounded nonempty tool catalog required")
	}
	sort.Slice(c.Tools, func(i, j int) bool { return c.Tools[i].Name < c.Tools[j].Name })
	seen := map[string]bool{}
	for _, t := range c.Tools {
		var doc map[string]json.RawMessage
		var name string
		if json.Unmarshal(t.Definition, &doc) != nil || json.Unmarshal(doc["name"], &name) != nil || name != t.NativeName || t.Name == "" || seen[t.Name] || len(doc["inputSchema"]) == 0 {
			return c, Fail(ContractMismatch, "invalid or duplicate native tool")
		}
		seen[t.Name] = true
	}
	c.Digest = Hash(c)
	if len(c.Digest) == 0 {
		return c, Fail(ContractMismatch, "invalid JSON in tool contract")
	}
	// Detach all caller-owned slices and maps.
	raw, _ := json.Marshal(c)
	_ = json.Unmarshal(raw, &c)
	return c, nil
}
func (c Contract) Tool(name string) (Tool, bool) {
	for _, t := range c.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
func (c Contract) Clone() Contract {
	raw, _ := json.Marshal(c)
	var out Contract
	_ = json.Unmarshal(raw, &out)
	return out
}

// Hash canonicalizes JSON only for digesting; forwarded definitions stay intact.
func Hash(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var obj any
	if dec.Decode(&obj) != nil {
		return ""
	}
	canonical, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("random identity: %v", err))
	}
	return hex.EncodeToString(b[:])
}
