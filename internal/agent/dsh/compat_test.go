package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDeveloperRoleCompatibilityMaterializedAndRecoveryBound(t *testing.T) {
	for _, name := range []string{"qwen3.8-flash", "deepseek-v4-flash"} {
		for _, effort := range []string{"", "low"} {
			for _, value := range []bool{false, true} {
				t.Run(name+"/"+effort+"/"+map[bool]string{false: "system", true: "developer"}[value], func(t *testing.T) {
					opts := fixtureOptions(t)
					opts.Model.ModelID, opts.Model.ReasoningEffort = name, effort
					opts.Model.Compat.SupportsDeveloperRole = &value
					// Materialization must affect exactly this model and preserve the
					// independent reasoning dialect, rather than disabling reasoning.
					data, _ := json.Marshal(profile(opts))
					var patches []struct {
						ID     string `json:"id"`
						Config struct {
							Providers map[string]struct {
								Reasoning string         `json:"reasoning"`
								Compat    map[string]any `json:"compat"`
								Models    []struct {
									ID     string         `json:"id"`
									Compat map[string]any `json:"compat"`
								} `json:"models"`
							} `json:"providers"`
						} `json:"config"`
					}
					if err := json.Unmarshal(data, &patches); err != nil {
						t.Fatal(err)
					}
					found := false
					for _, p := range patches {
						if p.ID != "llm-pi-ai" {
							continue
						}
						route := p.Config.Providers["teamx"]
						if len(route.Models) != 1 || route.Models[0].ID != name || route.Compat != nil || route.Reasoning != effort {
							t.Fatal("compat escaped exact model or replaced reasoning")
						}
						compat := route.Models[0].Compat
						if got, ok := compat["supportsDeveloperRole"]; !ok || got != value {
							t.Fatal("explicit false/true not preserved")
						}
						if name == "deepseek-v4-flash" && effort != "" && compat["thinkingFormat"] != "deepseek" {
							t.Fatal("compat discarded DeepSeek thinking format")
						}
						found = true
					}
					if !found {
						t.Fatal("missing Completions route")
					}
				})
			}
		}
	}
	opts := fixtureOptions(t)
	value := false
	opts.Model.Compat.SupportsDeveloperRole = &value
	s := openFixture(t, opts)
	// Open owns its copy; a caller's later change must not alter its identity.
	value = true
	if *s.opts.Model.Compat.SupportsDeveloperRole {
		t.Fatal("session aliases caller compatibility flag")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := CheckResumeConfig(opts); !errors.Is(err, ErrRecoveryUnsupported) {
		t.Fatal("changed role compatibility allowed original recovery", err)
	}
	value = false
	if err := CheckResumeConfig(opts); err != nil {
		t.Fatal("matching compatibility could not resume", err)
	}
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.id != s.id {
		t.Error("original Session ID changed")
	}
	if err := resumed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDeveloperRoleCompatibilityValidationAndClone(t *testing.T) {
	for _, value := range []bool{false, true} {
		opts := fixtureOptions(t)
		opts.Model.Protocol = "deepseek-messages"
		opts.Model.Compat.SupportsDeveloperRole = &value
		if err := CheckConfig(opts); err == nil || !strings.Contains(err.Error(), "requires openai-completions") {
			t.Fatal("unsupported protocol silently ignored compatibility", err)
		}
		opts.Model.Protocol = "openai-completions"
		clone := opts.Model.Clone()
		*clone.Compat.SupportsDeveloperRole = !value
		if *opts.Model.Compat.SupportsDeveloperRole != value {
			t.Fatal("clone aliases compatibility")
		}
	}
}
