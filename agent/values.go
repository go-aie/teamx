package agent

import (
	"encoding/json"
	"errors"
	"slices"
)

// Artifact optionally labels a build verified by the caller. When omitted,
// Open detects the runtime name and installed CLI version. Neither installs DSH.
type Artifact struct {
	Source  string `json:"source" toml:"source"`
	Version string `json:"version" toml:"version"`
}

type ModelConfig struct {
	// Protocol is "deepseek-messages" or "openai-completions".
	Protocol  string `json:"protocol" toml:"protocol"`
	BaseURL   string `json:"base_url,omitempty" toml:"base_url"`
	ModelID   string `json:"model_id" toml:"model_id"`
	APIKeyEnv string `json:"api_key_env" toml:"api_key_env"`
	// Zero retains the adapter's existing defaults. ContextWindow currently
	// configures the OpenAI-compatible model catalog only.
	ContextWindow int `json:"context_window,omitempty" toml:"context_window"`
	MaxTokens     int `json:"max_tokens,omitempty" toml:"max_tokens"`
	// Empty preserves the adapter/endpoint default. DeepSeek supports off, low,
	// high and max; other Completions models may also use minimal, medium or xhigh.
	ReasoningEffort string `json:"reasoning_effort,omitempty" toml:"reasoning_effort"`
	// Empty preserves the existing text-only configuration and recovery digest.
	// Image input is supported only by the OpenAI Completions adapter.
	InputModalities []string    `json:"input_modalities,omitempty" toml:"input_modalities"`
	Compat          ModelCompat `json:"compat,omitzero" toml:"compat"`
}

// ModelCompat contains explicit wire dialect choices for this exact model.
// Nil preserves existing adapter defaults; false must survive serialization.
type ModelCompat struct {
	SupportsDeveloperRole *bool `json:"supports_developer_role,omitempty" toml:"supports_developer_role"`
}

func (m ModelConfig) Clone() ModelConfig {
	m.InputModalities = slices.Clone(m.InputModalities)
	if flag := m.Compat.SupportsDeveloperRole; flag != nil {
		value := *flag
		m.Compat.SupportsDeveloperRole = &value
	}
	return m
}

// ContentBlock is one ACP text or inline raster image. Data contains raw image
// bytes; JSON serialization supplies canonical base64. URLs are not accepted.
// Callers must not mutate a prompt or its data until RunPrompt returns.
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Data     []byte `json:"data,omitempty"`
}

func (b ContentBlock) MarshalJSON() ([]byte, error) {
	if b.Type == "text" {
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{b.Type, b.Text})
	}
	if b.Type == "image" {
		return json.Marshal(struct {
			Type     string `json:"type"`
			MIMEType string `json:"mimeType"`
			Data     []byte `json:"data"`
		}{b.Type, b.MIMEType, b.Data})
	}
	return nil, errors.New("unsupported DSH prompt block type")
}

type TurnResult struct {
	StopReason       string       `json:"stopReason"`
	Text             string       `json:"text"`
	TextTruncated    bool         `json:"textTruncated,omitempty"`
	Thought          string       `json:"thought,omitempty"`
	ThoughtTruncated bool         `json:"thoughtTruncated,omitempty"`
	Tools            []ToolUpdate `json:"tools,omitempty"`
	ToolsTruncated   bool         `json:"toolsTruncated,omitempty"`
	// Events records activity in arrival order, including an empty list for no activity.
	Events          []ActivityEvent `json:"events"`
	EventsTruncated bool            `json:"eventsTruncated,omitempty"`
}

// ActivityEvent is one ordered diagnostic span. Consecutive text chunks of the
// same kind, or consecutive updates of the same tool, share a stable sequence.
// A tool update after another span remains a separate event at its arrival point.
type ActivityEvent struct {
	Seq           uint64      `json:"seq"`
	Kind          string      `json:"kind"` // thought, text, tool
	Text          string      `json:"text,omitempty"`
	TextTruncated bool        `json:"textTruncated,omitempty"`
	Tool          *ToolUpdate `json:"tool,omitempty"`
}

// ToolUpdate contains bounded operator diagnostics, never execution authority.
type ToolUpdate struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	Title            string `json:"title,omitempty"`
	Input            string `json:"input,omitempty"`
	Output           string `json:"output,omitempty"`
	DetailsTruncated bool   `json:"detailsTruncated,omitempty"`
}
