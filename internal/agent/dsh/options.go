// Package dsh owns one local DSH process and one ACP v1 session.
// It supports normal-session recovery, without a scheduling queue or crash takeover.
package dsh

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

var (
	ErrBusy                = errors.New("DSH session busy")
	ErrUnavailable         = errors.New("DSH session unavailable")
	ErrLocked              = errors.New("DSH state directory locked")
	ErrRecoveryUnsupported = errors.New("DSH original session recovery unavailable; require verified normal shutdown and matching backend state")
	ErrUncertain           = errors.New("DSH turn outcome may be uncertain; session disabled")
	ErrProtocol            = errors.New("invalid ACP protocol")
	ErrCancelled           = errors.New("DSH turn cancelled")
	ErrCleanupPending      = errors.New("DSH cleanup remains owned")
	ErrRateLimited         = errors.New("model service rate limited (HTTP 429); wait before retrying")
	ErrModelNotFound       = errors.New("configured model not found (HTTP 404, model_not_found); check model ID and endpoint")
	ErrModelUnavailable    = errors.New("configured model unavailable (HTTP 503, model_unavailable); no upstream endpoint available; check model availability or retry later")
	ErrAuthentication      = errors.New("model service authentication failed (HTTP 401); check API key and endpoint")
)

// HTTPStatusError retains an upstream HTTP failure when no known provider
// identifier supplies a more specific diagnostic. It excludes response bodies.
type HTTPStatusError struct{ StatusCode int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("model request failed (HTTP %d)", e.StatusCode)
}

// ClosedError retains a shutdown/recovery failure after every owned process,
// reader and worker has stopped. It permits an authorized fresh-session reset,
// but never authorizes resuming or retrying the failed original Session.
type ClosedError struct{ Err error }

func (e *ClosedError) Error() string { return e.Err.Error() }
func (e *ClosedError) Unwrap() error { return e.Err }

func CleanupSettled(err error) bool {
	var closed *ClosedError
	return err == nil || (!errors.Is(err, ErrCleanupPending) && errors.As(err, &closed))
}

// Artifact optionally labels a build verified by the caller. When omitted,
// Open detects the runtime name and installed CLI version. Neither installs DSH.
type Artifact struct {
	Source  string `json:"source" toml:"source"`
	Version string `json:"version" toml:"version"`
}

type Options struct {
	BinaryPath string
	// BinaryArgs precede --profile acp, e.g. the absolute CLI JS path for Node.
	// Arguments are passed directly to exec, never through a shell.
	BinaryArgs   []string
	Artifact     Artifact
	Workspace    string
	StateDir     string
	Instructions string
	Model        ModelConfig
	ToolEnv      map[string]string
	Permission   PermissionHandler
	Limits       Limits
	// Tests can supply an observer for their own explicitly created PIDs when
	// the host sandbox prohibits process-table access. Never exported as a
	// production option: real sessions must pass the platform observer gate.
	inspect func() (map[int]processIdentity, error)
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

func (m ModelConfig) imageInput() bool { return slices.Contains(m.InputModalities, "image") }

func (m ModelConfig) checkConfig() error {
	if m.Compat.SupportsDeveloperRole != nil && m.Protocol != "openai-completions" {
		return errors.New("model compat.supports_developer_role requires openai-completions")
	}
	seen := map[string]bool{}
	for _, modality := range m.InputModalities {
		if (modality != "text" && modality != "image") || seen[modality] {
			return errors.New("model input_modalities accepts unique text and image values only")
		}
		seen[modality] = true
	}
	if len(m.InputModalities) > 0 && !seen["text"] {
		return errors.New("model input_modalities must include text")
	}
	if seen["image"] && m.Protocol != "openai-completions" {
		return errors.New("model image input requires openai-completions")
	}
	switch m.ReasoningEffort {
	case "", "off", "low", "high", "max":
	case "minimal", "medium", "xhigh":
		if m.deepseek() {
			return errors.New("DeepSeek model reasoning_effort must be off, low, high or max")
		}
	default:
		return errors.New("model reasoning_effort must be off, minimal, low, medium, high, xhigh or max")
	}
	if m.ContextWindow < 0 || m.MaxTokens < 0 {
		return errors.New("model context_window and max_tokens must be positive when set")
	}
	if m.Protocol == "openai-completions" {
		contextWindow, maxTokens := m.tokenLimits()
		if maxTokens > contextWindow {
			return errors.New("model max_tokens must not exceed context_window")
		}
	} else if m.ContextWindow != 0 {
		return errors.New("model context_window requires openai-completions")
	}
	return nil
}

func (m ModelConfig) deepseek() bool {
	return m.Protocol == "deepseek-messages" || strings.HasPrefix(strings.ToLower(m.ModelID), "deepseek-")
}

func (m ModelConfig) tokenLimits() (contextWindow, maxTokens int) {
	contextWindow, maxTokens = m.ContextWindow, m.MaxTokens
	if contextWindow == 0 {
		contextWindow = 65536
	}
	if maxTokens == 0 {
		maxTokens = 4096
	}
	return
}

// DefaultTurnTimeout bounds the entire prompt, including model and tool time.
const DefaultTurnTimeout = 15 * time.Minute

type Limits struct {
	HandshakeTimeout time.Duration
	TurnTimeout      time.Duration

	// CancelTimeout bounds prompt cancellation, protocol close, and natural
	// process-tree exit separately. CleanupTimeout bounds each Close wait.
	CancelTimeout  time.Duration
	CleanupTimeout time.Duration

	WriteTimeout      time.Duration
	PermissionTimeout time.Duration
	FrameBytes        int
	InputBytes        int
	ImageBytes        int // Total raw image bytes per prompt, separate from text.
	TextBytes         int
}

func (l *Limits) defaults() error {
	durations := []struct {
		value    *time.Duration
		fallback time.Duration
	}{
		{&l.HandshakeTimeout, 15 * time.Second}, {&l.TurnTimeout, DefaultTurnTimeout},
		{&l.CancelTimeout, 5 * time.Second}, {&l.CleanupTimeout, 10 * time.Second},
		{&l.WriteTimeout, 2 * time.Second}, {&l.PermissionTimeout, 30 * time.Second},
	}
	for _, d := range durations {
		if *d.value < 0 {
			return errors.New("negative DSH timeout")
		}
		if *d.value == 0 {
			*d.value = d.fallback
		}
	}
	for _, size := range []struct {
		value         *int
		fallback, max int
	}{
		{&l.FrameBytes, 8 << 20, 8 << 20}, {&l.InputBytes, 1 << 20, 1 << 20}, {&l.ImageBytes, 4 << 20, 4 << 20}, {&l.TextBytes, 1 << 20, 1 << 20},
	} {
		if *size.value < 0 || *size.value > size.max {
			return errors.New("invalid DSH size limit")
		}
		if *size.value == 0 {
			*size.value = size.fallback
		}
	}
	return nil
}

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type PermissionRequest struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string `json:"toolCallId"`
	} `json:"toolCall"`
	Options []PermissionOption `json:"options"`
}

// PermissionHandler must return promptly when ctx ends. Only an optionId from
// this request can be selected. Nil, errors, invalid IDs and timeouts cancel.
// A handler that ignores cancellation retains cleanup ownership and the lock.
type PermissionHandler func(context.Context, PermissionRequest) (string, error)

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

// StopError preserves an ACP budget/refusal stop rather than reporting success.
type StopError struct{ Reason string }

func (e *StopError) Error() string { return "DSH stopped: " + e.Reason }

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func managedEnv(name, key string) bool {
	return name == key || strings.HasPrefix(name, "DSH_") || name == "DEEPSEEK_API_KEY" || name == "DEEPSEEK_BASE_URL"
}

// CheckConfig validates a team runtime without requiring installed artifacts or secrets.
func CheckConfig(opts Options) error {
	if err := opts.Model.checkConfig(); err != nil {
		return err
	}
	if !filepath.IsAbs(opts.BinaryPath) || strings.ContainsRune(opts.BinaryPath, 0) {
		return errors.New("absolute binary_path required")
	}
	if (opts.Artifact.Source == "") != (opts.Artifact.Version == "") {
		return errors.New("artifact source and version must be supplied together or omitted")
	}
	if opts.Model.Protocol != "deepseek-messages" && opts.Model.Protocol != "openai-completions" {
		return errors.New("unsupported DSH model protocol")
	}
	if strings.TrimSpace(opts.Model.ModelID) == "" || len(opts.Model.ModelID) > 1024 || !envName.MatchString(opts.Model.APIKeyEnv) || strings.HasPrefix(opts.Model.APIKeyEnv, "DSH_") {
		return errors.New("invalid model or api_key_env")
	}
	switch opts.Model.APIKeyEnv {
	case "PATH", "HOME", "PWD", "SHELL", "NODE_OPTIONS", "DEEPSEEK_BASE_URL":
		return errors.New("reserved credential environment name")
	}
	if opts.Model.BaseURL != "" {
		u, err := url.Parse(opts.Model.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid model base_url")
		}
	} else if opts.Model.Protocol == "openai-completions" {
		return errors.New("openai-completions requires base_url")
	}
	for _, arg := range opts.BinaryArgs {
		if strings.ContainsRune(arg, 0) || arg == "--profile" || strings.HasPrefix(arg, "--profile=") || arg == "--patch" || strings.HasPrefix(arg, "--patch=") {
			return errors.New("invalid binary_args")
		}
		if (strings.HasSuffix(arg, ".js") || strings.HasSuffix(arg, ".mjs")) && !filepath.IsAbs(arg) {
			return errors.New("DSH script arguments must be absolute")
		}
	}
	return nil
}

// CheckToolEnv rejects values that would override the backend's managed environment.
func CheckToolEnv(env map[string]string, credential string) error {
	for name, value := range env {
		if !envName.MatchString(name) || strings.ContainsRune(value, 0) || managedEnv(name, credential) {
			return fmt.Errorf("invalid or reserved tool_env name: %s", name)
		}
	}
	return nil
}

func validate(opts Options) (Options, error) {
	if err := opts.Limits.defaults(); err != nil {
		return opts, err
	}
	for _, p := range []string{opts.BinaryPath, opts.Workspace, opts.StateDir} {
		if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
			return opts, errors.New("DSH paths must be absolute")
		}
	}
	bin, err := filepath.EvalSymlinks(opts.BinaryPath)
	if err != nil {
		return opts, err
	}
	info, err := os.Stat(bin)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return opts, errors.New("invalid DSH executable")
	}
	opts.BinaryPath = bin
	workspace, err := filepath.EvalSymlinks(opts.Workspace)
	if err != nil {
		return opts, err
	}
	info, err = os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return opts, errors.New("invalid DSH workspace")
	}
	opts.Workspace = workspace
	opts.StateDir = filepath.Clean(opts.StateDir)
	if (opts.Artifact.Source == "") != (opts.Artifact.Version == "") {
		return opts, errors.New("artifact source and version must be supplied together or omitted")
	}
	if len(opts.Instructions) > opts.Limits.InputBytes {
		return opts, errors.New("DSH instructions too large")
	}
	if opts.Model.Protocol == "" {
		opts.Model.Protocol = "deepseek-messages"
	}
	if err := opts.Model.checkConfig(); err != nil {
		return opts, err
	}
	if opts.Model.Protocol != "deepseek-messages" && opts.Model.Protocol != "openai-completions" {
		return opts, errors.New("unsupported DSH model protocol")
	}
	if strings.TrimSpace(opts.Model.ModelID) == "" || len(opts.Model.ModelID) > 1024 || !envName.MatchString(opts.Model.APIKeyEnv) || strings.HasPrefix(opts.Model.APIKeyEnv, "DSH_") {
		return opts, errors.New("invalid DSH model or credential binding")
	}
	switch opts.Model.APIKeyEnv {
	case "PATH", "HOME", "PWD", "SHELL", "NODE_OPTIONS", "DEEPSEEK_BASE_URL":
		return opts, errors.New("reserved DSH credential environment name")
	}
	if os.Getenv(opts.Model.APIKeyEnv) == "" {
		return opts, errors.New("DSH credential environment is empty")
	}
	if opts.Model.BaseURL != "" {
		u, err := url.Parse(opts.Model.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return opts, errors.New("invalid DSH base URL")
		}
	} else if opts.Model.Protocol == "openai-completions" {
		return opts, errors.New("openai-completions requires a base URL")
	}
	for name, value := range opts.ToolEnv {
		if !envName.MatchString(name) || strings.ContainsRune(value, 0) || managedEnv(name, opts.Model.APIKeyEnv) {
			return opts, fmt.Errorf("invalid or reserved DSH tool environment name: %s", name)
		}
	}
	for _, arg := range opts.BinaryArgs {
		if strings.ContainsRune(arg, 0) || arg == "--profile" || strings.HasPrefix(arg, "--profile=") || arg == "--patch" || strings.HasPrefix(arg, "--patch=") {
			return opts, errors.New("invalid DSH binary argument")
		}
	}
	// Retain our own copies; callers may reuse their configuration after Open.
	opts.BinaryArgs = append([]string(nil), opts.BinaryArgs...)
	opts.Model = opts.Model.Clone()
	toolEnv := make(map[string]string, len(opts.ToolEnv))
	for k, v := range opts.ToolEnv {
		toolEnv[k] = v
	}
	opts.ToolEnv = toolEnv
	return opts, nil
}

func environment(opts Options) []string {
	env := map[string]string{}
	for _, item := range os.Environ() {
		name, value, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(name, "DSH_") && name != "DEEPSEEK_BASE_URL" {
			env[name] = value
		}
	}
	for name, value := range opts.ToolEnv {
		if name == "PATH" {
			if value != "" {
				value += string(os.PathListSeparator) + env[name]
			} else {
				value = env[name]
			}
		}
		env[name] = value
	}
	env["DSH_HOME"] = filepath.Join(opts.StateDir, "home")
	env["DSH_AGENTS_HOME"] = filepath.Join(opts.StateDir, "agents")
	env["DSH_PERMISSION_MODE"] = "workspace-write"
	env["DSH_TELEMETRY_DISABLED"] = "1"
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(env))
	for _, name := range names {
		result = append(result, name+"="+env[name])
	}
	return result
}
