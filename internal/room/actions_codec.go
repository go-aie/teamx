package room

import (
	"bytes"
	"encoding/json"

	"github.com/go-aie/teamx/internal/platform"
)

// Historical self-delivery receipts retain their original intent and result.
// Compatibility is confined to decoding; execution uses the generic basis.
func (a *referenceAction) UnmarshalJSON(data []byte) error {
	type plain referenceAction
	var wire struct {
		plain
		Legacy *platform.CapabilityEvidence `json:"self_delivery"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&wire); err != nil {
		return err
	}
	*a = referenceAction(wire.plain)
	if wire.Legacy != nil {
		if a.CapabilityBasis != nil || a.Spec.BindingEvidence != "" {
			return ErrInvalid
		}
		wire.Legacy.Kind = "self_delivery"
		wire.Legacy.AccountOwnerConfirmed = true
		a.CapabilityBasis = wire.Legacy
		a.Spec.BindingEvidence = "self_delivery"
	}
	return nil
}
