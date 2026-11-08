package platform

// CapabilityEvidence is server-owned proof captured when an operator enables
// a capability for an exact account and target. Native relationships are
// established and rechecked by the adapter, never inferred by the common core.
type CapabilityEvidence struct {
	AccountOwnerConfirmed bool            `json:"account_owner_confirmed,omitempty"`
	Kind                  string          `json:"kind"`
	CredentialVersion     uint64          `json:"credential_version,omitempty"`
	Account               AccountIdentity `json:"account"`
	Target                TargetIdentity  `json:"target"`
	Operator              string          `json:"operator"`
}

func (e *CapabilityEvidence) Matches(kind string, a AccessContext, t TargetIdentity) bool {
	if e == nil || kind == "" || e.Kind != kind || !identityText(e.Operator) || !e.Account.Valid() || e.Account != a.Account || !t.Valid() {
		return false
	}
	x, err := e.Target.Key()
	y, targetErr := t.Key()
	// Version zero can be decoded as historical evidence, but cannot admit a
	// current execution. Credential rotation invalidates previously enabled proof.
	return err == nil && targetErr == nil && x == y && (e.CredentialVersion == 0 || e.CredentialVersion == a.CredentialVersion)
}

func (e *CapabilityEvidence) Current(kind string, a AccessContext, t TargetIdentity) bool {
	return e != nil && e.CredentialVersion > 0 && e.Matches(kind, a, t)
}
