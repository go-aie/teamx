package notification

import (
	"encoding/json"

	"github.com/go-aie/teamx/internal/platform"
)

type AdmissionDecision string

const (
	AdmissionAllow    AdmissionDecision = "allow"
	AdmissionExcluded AdmissionDecision = "excluded"
)

// Verification is independent of the decision: missing native evidence does
// not establish either permission to process or a reason to exclude an item.
type Admission struct {
	Verified   bool              `json:"verified"`
	Decision   AdmissionDecision `json:"decision,omitempty"`
	ReasonCode string            `json:"reason_code,omitempty"`
}

func (a Admission) Valid() bool {
	return (!a.Verified && a.Decision == "") ||
		(a.Verified && (a.Decision == AdmissionAllow || a.Decision == AdmissionExcluded))
}

// EvidencePath is an adapter-owned, account-scoped relative snapshot path.
// Inspect is pure: it validates opaque native evidence, never does I/O. Missing
// evidence must remain unverified when the adapter requires a native check.
type AdmissionCapability struct {
	EvidencePath func(platform.SourceRef) string
	Inspect      func(Item, json.RawMessage) (Admission, error)
}

// Capabilities are independently optional. Project supports provider-owned
// saved-envelope decoding; MatchAction verifies native message/action scope.
// Common projection, scheduling and receipt handling contain no provider rules.
type Capabilities struct {
	Admission   *AdmissionCapability
	Project     func(Item) View
	MatchAction func(View, platform.TargetActionRequest, platform.AccessContext) bool
}
