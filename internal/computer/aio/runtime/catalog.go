package runtime

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/go-aie/teamx/internal/computer"
)

type Policy struct{ Actions map[string]bool }

func LoadCatalog(toolsPath, versionsPath string) (computer.Contract, Policy, error) {
	var lock struct {
		SelectedNames []string          `json:"selected_names"`
		Tools         []json.RawMessage `json:"selected_tools"`
		GUI           struct {
			Actions []string `json:"allowed_action_types"`
		} `json:"gui_action_policy"`
	}
	raw, err := os.ReadFile(toolsPath)
	if err != nil {
		return computer.Contract{}, Policy{}, err
	}
	if err = json.Unmarshal(raw, &lock); err != nil {
		return computer.Contract{}, Policy{}, err
	}
	versionsRaw, err := os.ReadFile(versionsPath)
	if err != nil {
		return computer.Contract{}, Policy{}, err
	}
	var versionsDoc map[string]json.RawMessage
	if err = json.Unmarshal(versionsRaw, &versionsDoc); err != nil {
		return computer.Contract{}, Policy{}, err
	}
	versions := map[string]string{"runtime_protocol": "1"}
	for k, v := range versionsDoc {
		var text string
		if json.Unmarshal(v, &text) != nil {
			text = string(v)
		}
		versions[k] = text
	}
	// Audited against the fixed build; never infer this from annotations.
	readonly := map[string]bool{"browser_gui_screenshot": true, "browser_screenshot": true, "browser_get_markdown": true, "browser_get_text": true, "browser_read_links": true, "browser_tab_list": true, "browser_get_download_list": true, "browser_get_clickable_elements": true}
	selected := map[string]bool{}
	for _, name := range lock.SelectedNames {
		selected[name] = true
	}
	var tools []computer.Tool
	for _, doc := range lock.Tools {
		var t struct {
			Name string `json:"name"`
		}
		if err = json.Unmarshal(doc, &t); err != nil {
			return computer.Contract{}, Policy{}, err
		}
		if !selected[t.Name] {
			return computer.Contract{}, Policy{}, fmt.Errorf("tool not selected: %s", t.Name)
		}
		tools = append(tools, computer.Tool{Name: t.Name, NativeName: t.Name, Definition: doc, ReadOnly: readonly[t.Name], RequiresImages: t.Name == "browser_gui_screenshot" || t.Name == "browser_gui_execute_action" || t.Name == "browser_screenshot"})
	}
	if len(tools) != len(selected) {
		return computer.Contract{}, Policy{}, fmt.Errorf("selected tools mismatch")
	}
	policy := Policy{Actions: map[string]bool{}}
	for _, a := range lock.GUI.Actions {
		policy.Actions[a] = true
	}
	versions["admission_policy_digest"] = computer.Hash(policy.Actions)
	contract, err := computer.NewContract("aio", versions, tools)
	return contract, policy, err
}
func (p Policy) Validate(tool string, args json.RawMessage) error {
	if tool != "browser_gui_execute_action" {
		return nil
	}
	var doc struct {
		Action struct {
			Type string `json:"action_type"`
		} `json:"action"`
	}
	if json.Unmarshal(args, &doc) != nil || !p.Actions[doc.Action.Type] {
		return computer.Fail(computer.Invalid, "GUI action is outside the audited action policy")
	}
	return nil
}
