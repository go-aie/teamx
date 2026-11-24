package computer

import (
	"encoding/json"
	"testing"
)

func TestNativeContractPreservesExtensionsAndFreezesExecutionPolicy(t *testing.T) {
	raw := json.RawMessage(`{"name":"native","inputSchema":{"type":"object","properties":{"n":{"type":"integer"}}},"outputSchema":{"type":"object"},"annotations":{"readOnlyHint":true},"vendor_extension":{"a":1,"z":2}}`)
	tools := []Tool{{Name: "mapped", NativeName: "native", Definition: raw}}
	original, err := NewContract("vendor", map[string]string{"build": "fixed"}, tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(original.Tools[0].Definition) != string(raw) {
		t.Fatal("native schema was rewritten")
	}
	// JSON key order and formatting do not change a contract, but unknown native
	// fields, accepted policy and execution versions all do.
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	reformatted, _ := json.MarshalIndent(doc, "", "  ")
	tools[0].Definition = reformatted
	same, err := NewContract("vendor", map[string]string{"build": "fixed"}, tools)
	if err != nil || same.Digest != original.Digest {
		t.Fatal("unstable canonical contract")
	}
	tools[0].ReadOnly = true
	changed, _ := NewContract("vendor", map[string]string{"build": "fixed"}, tools)
	if changed.Digest == same.Digest {
		t.Fatal("policy change missed")
	}
	tools[0].ReadOnly = false
	doc["vendor_extension"] = false
	tools[0].Definition, _ = json.Marshal(doc)
	changed, _ = NewContract("vendor", map[string]string{"build": "fixed"}, tools)
	if changed.Digest == same.Digest {
		t.Fatal("native extension lost from contract")
	}
	original.Versions["build"] = "edited"
	if same.Versions["build"] != "fixed" {
		t.Fatal("caller mutates accepted snapshot")
	}
}
