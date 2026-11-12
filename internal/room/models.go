package room

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Management parameters deliberately omit api_key_env. It is transport owned
// by materialization, not a user-maintained field.
type ModelParameters struct {
	Protocol        string          `json:"protocol" toml:"protocol"`
	BaseURL         string          `json:"base_url,omitempty" toml:"base_url,omitempty"`
	ModelID         string          `json:"model_id" toml:"model_id"`
	InputModalities []string        `json:"input_modalities,omitempty" toml:"input_modalities,omitempty"`
	ContextWindow   int             `json:"context_window,omitempty" toml:"context_window,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty" toml:"max_tokens,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty" toml:"reasoning_effort,omitempty"`
	Compat          dsh.ModelCompat `json:"compat,omitzero" toml:"compat,omitempty"`
}

func (p ModelParameters) materialize() dsh.ModelConfig {
	model := (dsh.ModelConfig{Protocol: p.Protocol, BaseURL: p.BaseURL, ModelID: p.ModelID, APIKeyEnv: "OPENAI_API_KEY", InputModalities: p.InputModalities, ContextWindow: p.ContextWindow, MaxTokens: p.MaxTokens, ReasoningEffort: p.ReasoningEffort, Compat: p.Compat}).Clone()
	if len(model.InputModalities) == 0 {
		model.InputModalities = nil
	}
	return model
}
func (p ModelParameters) clone() ModelParameters {
	p.InputModalities = slices.Clone(p.InputModalities)
	if len(p.InputModalities) == 0 {
		p.InputModalities = nil
	}
	if p.Compat.SupportsDeveloperRole != nil {
		v := *p.Compat.SupportsDeveloperRole
		p.Compat.SupportsDeveloperRole = &v
	}
	return p
}

type modelEntry struct {
	ID              string `json:"id" toml:"id"`
	Name            string `json:"name" toml:"name"`
	Revision        uint64 `json:"revision" toml:"revision"`
	CredentialRef   string `json:"credential_ref" toml:"credential_ref"`
	PendingDeletion bool   `json:"pending_deletion,omitempty" toml:"pending_deletion,omitempty"`
	Unverified      bool   `json:"unverified,omitempty" toml:"unverified,omitempty"`
	ModelParameters
}

func (e modelEntry) clone() modelEntry { e.ModelParameters = e.ModelParameters.clone(); return e }
func (e modelEntry) sameExecution(other modelEntry) bool {
	return e.CredentialRef == other.CredentialRef && reflect.DeepEqual(e.ModelParameters.materialize(), other.ModelParameters.materialize())
}

// No name, selection mode or source version participates in execution identity.
func (e modelEntry) executionDigest() string {
	data, _ := json.Marshal(struct {
		Model      dsh.ModelConfig
		Credential string
	}{e.materialize(), e.CredentialRef})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type modelCatalog struct {
	SchemaVersion int          `json:"schema_version" toml:"schema_version"`
	Version       uint64       `json:"version" toml:"version"`
	Default       string       `json:"default" toml:"default"`
	Entries       []modelEntry `json:"entries" toml:"entries"`
}

func (c modelCatalog) clone() modelCatalog {
	c.Entries = slices.Clone(c.Entries)
	for i := range c.Entries {
		c.Entries[i] = c.Entries[i].clone()
	}
	return c
}

var modelEntryID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func (c modelCatalog) validate() error {
	if c.SchemaVersion != 1 || len(c.Entries) > 256 {
		return invalidField("models", "unsupported model catalog schema or size")
	}
	if len(c.Entries) == 0 {
		if c.Default != "" {
			return ErrInvalid
		}
		return nil
	}
	if c.Version == 0 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	defaultOK := false
	for _, e := range c.Entries {
		if !modelEntryID.MatchString(e.ID) || seen[e.ID] || strings.TrimSpace(e.Name) == "" || len(e.Name) > 256 || e.Revision == 0 || !validCredentialRef(e.CredentialRef) {
			return invalidField("models.entries", "invalid model identity, name, revision or credential reference")
		}
		seen[e.ID] = true
		if err := dsh.CheckConfig(dsh.Options{BinaryPath: "/model-validation", Model: e.materialize()}); err != nil {
			return fmt.Errorf("model entry: %w", err)
		}
		defaultOK = defaultOK || e.ID == c.Default && !e.PendingDeletion
	}
	if !defaultOK {
		return invalidField("models.default", "choose an available default model")
	}
	return nil
}

type modelSelection struct {
	Mode    string `json:"mode"`
	EntryID string `json:"entry_id,omitempty"`
}

func (s modelSelection) validate() error {
	if s.Mode == "default" && s.EntryID == "" || s.Mode == "explicit" && modelEntryID.MatchString(s.EntryID) {
		return nil
	}
	return invalidField("model_selection", "choose default or a model entry")
}
func (c modelCatalog) resolve(s modelSelection) (modelEntry, error) {
	if err := s.validate(); err != nil {
		return modelEntry{}, err
	}
	id := s.EntryID
	if s.Mode == "default" {
		id = c.Default
	}
	for _, e := range c.Entries {
		if e.ID == id && !e.PendingDeletion {
			return e.clone(), nil
		}
	}
	return modelEntry{}, invalidField("model_selection", "selected model is unavailable")
}

// Impacts are computed by full member identity; names and roles are irrelevant.
type memberModelReference struct {
	MemberIdentity
	Selection modelSelection
	Version   uint64
}

func modelImpacts(before, after modelCatalog, refs []memberModelReference) ([]MemberIdentity, error) {
	var changed []MemberIdentity
	seen := map[MemberIdentity]bool{}
	for _, ref := range refs {
		if ref.Version == 0 || seen[ref.MemberIdentity] || validateMemberHeader(formatVersion, ref.MemberIdentity) != nil {
			return nil, ErrIdentity
		}
		seen[ref.MemberIdentity] = true
		old, err := before.resolve(ref.Selection)
		if err != nil {
			return nil, err
		}
		next, err := after.resolve(ref.Selection)
		if err != nil {
			return nil, err
		}
		if !old.sameExecution(next) {
			changed = append(changed, ref.MemberIdentity)
		}
	}
	return changed, nil
}

// Migration copies the existing three-state compat without guessing defaults.
func parametersFromLegacyModel(m dsh.ModelConfig) ModelParameters {
	return ModelParameters{Protocol: m.Protocol, BaseURL: m.BaseURL, ModelID: m.ModelID, InputModalities: m.Clone().InputModalities, ContextWindow: m.ContextWindow, MaxTokens: m.MaxTokens, ReasoningEffort: m.ReasoningEffort, Compat: m.Clone().Compat}
}
