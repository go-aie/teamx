package dsh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredReasoningEffortMaterializedAndRecoveryBound(t *testing.T) {
	for _, protocol := range []string{"openai-completions", "deepseek-messages"} {
		for _, effort := range []string{"off", "low", "high", "max"} {
			t.Run(protocol+"/"+effort, func(t *testing.T) {
				opts := fixtureOptions(t)
				opts.Model.Protocol, opts.Model.ModelID, opts.Model.ReasoningEffort = protocol, "deepseek-v4-flash", effort
				opts.ToolEnv["TEAMX_DSH_TEST_MODEL"] = string(mustJSON([]string{provider(opts.Model), opts.Model.ModelID}))
				if err := CheckConfig(opts); err != nil {
					t.Fatal(err)
				}
				s := openFixture(t, opts)
				defer s.Close(context.Background())
				data, err := os.ReadFile(filepath.Join(opts.StateDir, "home", "cordis.patch.yml"))
				var patches []struct {
					ID     string `json:"id"`
					Config struct {
						ReasoningEffort string `json:"reasoningEffort"`
						Providers       map[string]struct {
							Reasoning string `json:"reasoning"`
							Models    []struct {
								ReasoningEfforts map[string]*string `json:"reasoningEfforts"`
								Compat           struct {
									ThinkingFormat        string `json:"thinkingFormat"`
									SupportsDeveloperRole *bool  `json:"supportsDeveloperRole"`
								} `json:"compat"`
							} `json:"models"`
						} `json:"providers"`
					} `json:"config"`
				}
				if err != nil || json.Unmarshal(data, &patches) != nil {
					t.Fatal(err, string(data))
				}
				found := false
				for _, patch := range patches {
					if protocol == "deepseek-messages" && patch.ID == "llm-deepseek" {
						found = patch.Config.ReasoningEffort == effort
					}
					if protocol != "openai-completions" || patch.ID != "llm-pi-ai" {
						continue
					}
					route := patch.Config.Providers["teamx"]
					if route.Reasoning != effort || len(route.Models) != 1 {
						t.Fatal("missing configured reasoning", string(data))
					}
					model := route.Models[0]
					if off, ok := model.ReasoningEfforts["off"]; !ok || off != nil || model.ReasoningEfforts["high"] == nil {
						t.Fatal("off must be declared alongside a thinking level", string(data))
					}
					if effort != "off" && (model.ReasoningEfforts[effort] == nil || *model.ReasoningEfforts[effort] != effort) {
						t.Fatal("missing effort wire spelling", string(data))
					}
					if model.Compat.ThinkingFormat != "deepseek" || model.Compat.SupportsDeveloperRole == nil || *model.Compat.SupportsDeveloperRole {
						t.Fatal("DeepSeek requires explicit thinking control and system role", string(data))
					}
					found = true
				}
				if !found {
					t.Fatal("missing reasoning configuration", string(data))
				}
				if err := s.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := CheckResumeConfig(opts); err != nil {
					t.Fatal(err)
				}
				opts.Model.ReasoningEffort = ""
				if err := CheckResumeConfig(opts); !errors.Is(err, ErrRecoveryUnsupported) {
					t.Fatalf("changed reasoning accepted for original session: %v", err)
				}
			})
		}
	}
}

func TestReasoningEffortValidation(t *testing.T) {
	for _, protocol := range []string{"openai-completions", "deepseek-messages"} {
		for _, model := range []string{"deepseek-v4-flash", "other-model"} {
			for _, effort := range []string{"", "off", "minimal", "low", "medium", "high", "xhigh", "max", "LOW", "none", " low"} {
				opts := Options{BinaryPath: "/fixture/dsh", Artifact: Artifact{Source: "fixture", Version: "test"},
					Model: ModelConfig{Protocol: protocol, BaseURL: "https://example.invalid/v1", ModelID: model, APIKeyEnv: "FIXTURE_KEY", ReasoningEffort: effort}}
				valid := effort == "" || effort == "off" || effort == "low" || effort == "high" || effort == "max"
				if protocol == "openai-completions" && model == "other-model" {
					valid = valid || effort == "minimal" || effort == "medium" || effort == "xhigh"
				}
				if err := CheckConfig(opts); (err == nil) != valid {
					t.Fatalf("%s/%s/%q: valid=%v, error=%v", protocol, model, effort, valid, err)
				}
			}
		}
	}
}

func TestUnconfiguredReasoningPreservesLegacyRecoveryDigest(t *testing.T) {
	opts := Options{Instructions: "role", Model: ModelConfig{Protocol: "openai-completions", BaseURL: "https://example.invalid/v1", ModelID: "deepseek-v4-flash", APIKeyEnv: "FIXTURE_KEY"}}
	// Serialized before reasoning_effort existed. Old normal sessions must
	// retain the same recovery fingerprint and materialized provider config.
	legacy := []byte(`{"Instructions":"role","Model":{"protocol":"openai-completions","base_url":"https://example.invalid/v1","model_id":"deepseek-v4-flash","api_key_env":"FIXTURE_KEY"}}`)
	hash := sha256.Sum256(legacy)
	if configDigest(opts) != hex.EncodeToString(hash[:]) {
		t.Fatal("unset reasoning changed original session fingerprint")
	}
	data, err := json.Marshal(profile(opts))
	if err != nil {
		t.Fatal(err)
	}
	const legacyProfile = `[{"config":{"model":"deepseek-v4-flash","provider":"teamx"},"id":"acp"},{"config":{"policy":"ask"},"id":"approval"},{"config":{"mode":"workspace-write"},"id":"sandbox-policy"},{"disabled":true,"id":"llm-deepseek"},{"config":{"providers":{"teamx":{"api":"openai-completions","apiKeyEnv":"FIXTURE_KEY","baseURL":"https://example.invalid/v1","models":[{"contextWindow":65536,"id":"deepseek-v4-flash","maxTokens":4096}],"retryPolicy":{"maxRetries":0,"mode":"normal"}}}},"id":"llm-pi-ai"}]`
	if string(data) != legacyProfile {
		t.Fatal("unset reasoning changed original session profile", string(data))
	}
}
