package tool

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/go-aie/teamx/internal/platform"
)

// Binding is one explicitly selected tool and optional connection per member.
// CapabilityEvidence is generated only by the server's registered adapters.
type Binding struct {
	CapabilityEvidence   []platform.CapabilityEvidence `json:"capability_evidence,omitempty"`
	ActionAuthorization  *ActionAuthorization          `json:"action_authorization,omitempty"`
	AgentID              string                        `json:"agent_id"`
	MembershipID         string                        `json:"membership_id"`
	Tool                 string                        `json:"tool"`
	ConnectionID         string                        `json:"connection_id,omitempty"`
	ConnectionGeneration string                        `json:"connection_generation,omitempty"`
}

func (b Binding) Evidence(kind string) *platform.CapabilityEvidence {
	for i := range b.CapabilityEvidence {
		if b.CapabilityEvidence[i].Kind == kind {
			return &b.CapabilityEvidence[i]
		}
	}
	return nil
}

// Decode the former field only at the persistence/wire boundary. New writes
// use the generic contract; this does not grant or renew historical evidence.
func (b *Binding) UnmarshalJSON(data []byte) error {
	type plain Binding
	var wire struct {
		plain
		Legacy *platform.CapabilityEvidence `json:"self_delivery"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&wire); err != nil {
		return err
	}
	*b = Binding(wire.plain)
	if wire.Legacy != nil {
		if len(b.CapabilityEvidence) != 0 {
			return fmt.Errorf("conflicting capability evidence")
		}
		wire.Legacy.Kind = "self_delivery"
		wire.Legacy.AccountOwnerConfirmed = true
		b.CapabilityEvidence = []platform.CapabilityEvidence{*wire.Legacy}
	}
	return nil
}
