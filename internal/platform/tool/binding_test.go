package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBindingDecodesHistoricalEvidenceWithoutRenewal(t *testing.T) {
	var b Binding
	if err := json.Unmarshal([]byte(`{"tool":"lark","self_delivery":{"operator":"human","account":{},"target":{}}}`), &b); err != nil {
		t.Fatal(err)
	}
	e := b.Evidence("self_delivery")
	if e == nil || e.CredentialVersion != 0 || !e.AccountOwnerConfirmed {
		t.Fatal("historical proof was lost or renewed", b)
	}
	encoded, err := json.Marshal(b)
	if err != nil || strings.Contains(string(encoded), `"self_delivery":`) || !strings.Contains(string(encoded), `"capability_evidence":`) {
		t.Fatal("legacy contract escaped the decoder", string(encoded), err)
	}
	for _, raw := range []string{
		`{"tool":"memo","unexpected":true}`,
		`{"capability_evidence":[{"kind":"other"}],"self_delivery":{}}`,
	} {
		if err := json.Unmarshal([]byte(raw), &b); err == nil {
			t.Fatal("ambiguous or unknown binding input accepted", raw)
		}
	}
}
