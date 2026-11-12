package dsh

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type reference struct {
	Version      int             `json:"version"`
	Artifact     Artifact        `json:"artifact"`
	BinaryPath   string          `json:"binaryPath"`
	BinaryArgs   []string        `json:"binaryArgs,omitempty"`
	BinarySHA256 string          `json:"binarySHA256"`
	AgentInfo    json.RawMessage `json:"agentInfo"`
	Workspace    string          `json:"workspace"`
	SessionID    string          `json:"sessionId"`
	ConfigSHA256 string          `json:"configSHA256,omitempty"`
	Stopped      bool            `json:"stopped,omitempty"`
}

func runtimeArtifact(info json.RawMessage) (Artifact, error) {
	var identity struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(info, &identity); err != nil || strings.TrimSpace(identity.Name) == "" ||
		strings.TrimSpace(identity.Version) == "" || len(identity.Name) > 1024 || len(identity.Version) > 1024 {
		return Artifact{}, fmt.Errorf("%w: DSH must advertise its runtime name and version", ErrProtocol)
	}
	return Artifact{Source: identity.Name, Version: identity.Version}, nil
}

// CheckResume checks durable shutdown evidence without launching a process.
// Resume repeats this check under the directory lock and validates configuration.
func CheckResume(dir string) error {
	lock, err := lockDirectory(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	_, err = recoveryReference(dir)
	return err
}

// CheckReset verifies that the old execution has stopped. Unlike CheckResume,
// it allows discarding an unhealthy Session after an operator reset or a bounded
// service recovery that preserves enabled intent and verifies action outcomes.
func CheckReset(dir string) error {
	lock, err := lockDirectory(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := recoveryReference(dir); err == nil {
		return nil // Normal shutdown evidence from older versions is sufficient.
	}
	f, err := os.Open(filepath.Join(dir, "settled.json"))
	if err != nil {
		return fmt.Errorf("%w: old execution has no verified settlement", ErrRecoveryUnsupported)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256 {
		return fmt.Errorf("%w: invalid settlement evidence", ErrRecoveryUnsupported)
	}
	var record struct {
		Version int `json:"version"`
	}
	dec := json.NewDecoder(io.LimitReader(f, 256))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil || record.Version != 1 || dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("%w: invalid settlement evidence", ErrRecoveryUnsupported)
	}
	return nil
}

func recoveryReference(dir string) (reference, error) {
	var ref reference
	f, err := os.Open(filepath.Join(dir, "backend.json"))
	if err != nil {
		return ref, fmt.Errorf("%w: missing backend reference", ErrRecoveryUnsupported)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return ref, fmt.Errorf("%w: oversized or invalid backend reference", ErrRecoveryUnsupported)
	}
	dec := json.NewDecoder(io.LimitReader(f, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ref); err != nil {
		return ref, fmt.Errorf("%w: invalid backend reference", ErrRecoveryUnsupported)
	}
	if dec.Decode(new(any)) != io.EOF || ref.Version != 1 || ref.SessionID == "" || len(ref.SessionID) > 1024 || !ref.Stopped || ref.ConfigSHA256 == "" {
		return ref, fmt.Errorf("%w: no verified normal shutdown", ErrRecoveryUnsupported)
	}
	return ref, nil
}

func configDigest(opts Options) string {
	ref := ""
	if opts.Credential != nil {
		ref = opts.Credential.Ref
	}
	data, _ := json.Marshal(struct {
		Instructions  string
		Model         ModelConfig
		CredentialRef string `json:",omitempty"`
	}{opts.Instructions, opts.Model, ref})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// CheckResumeConfig verifies original-session compatibility without launching or consuming evidence.
func CheckResumeConfig(opts Options) error {
	var err error
	opts.BinaryPath, err = filepath.EvalSymlinks(opts.BinaryPath)
	if err != nil {
		return err
	}
	opts.Workspace, err = filepath.EvalSymlinks(opts.Workspace)
	if err != nil {
		return err
	}
	if opts.Model.Protocol == "" {
		opts.Model.Protocol = "deepseek-messages"
	}
	lock, err := lockDirectory(opts.StateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	ref, err := recoveryReference(opts.StateDir)
	if err != nil {
		return err
	}
	return checkResumeConfig(&opts, ref)
}
func checkResumeConfig(opts *Options, ref reference) error {
	dir := opts.StateDir
	hash, err := executableDigest(opts.BinaryPath)
	if err != nil {
		return err
	}
	if (opts.Artifact != (Artifact{}) && ref.Artifact != opts.Artifact) || ref.BinaryPath != opts.BinaryPath || !slices.Equal(ref.BinaryArgs, opts.BinaryArgs) ||
		ref.BinarySHA256 != hash || ref.Workspace != opts.Workspace || ref.ConfigSHA256 != configDigest(*opts) {
		return fmt.Errorf("%w: backend configuration changed", ErrRecoveryUnsupported)
	}
	if _, err := runtimeArtifact(ref.AgentInfo); err != nil {
		return fmt.Errorf("%w: missing recorded DSH runtime identity", ErrRecoveryUnsupported)
	}
	for _, name := range []string{"home", "agents"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("%w: missing backend history directory", ErrRecoveryUnsupported)
		}
	}
	patch, err := json.MarshalIndent(profile(*opts), "", "  ")
	if err != nil {
		return err
	}
	for name, expected := range map[string][]byte{"AGENTS.md": []byte(opts.Instructions), "cordis.patch.yml": append(patch, '\n')} {
		f, err := os.Open(filepath.Join(dir, "home", name))
		if err != nil {
			return fmt.Errorf("%w: missing backend configuration", ErrRecoveryUnsupported)
		}
		data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		_ = f.Close()
		if readErr != nil || !bytes.Equal(data, expected) {
			return fmt.Errorf("%w: materialized backend configuration changed", ErrRecoveryUnsupported)
		}
	}
	return nil
}

func prepare(opts *Options, resume bool) (*os.File, reference, error) {
	var ref reference
	if err := os.MkdirAll(opts.StateDir, 0700); err != nil {
		return nil, ref, err
	}
	dir, err := filepath.EvalSymlinks(opts.StateDir)
	if err != nil {
		return nil, ref, err
	}
	opts.StateDir = dir
	lock, err := lockDirectory(dir)
	if err != nil {
		return nil, ref, err
	}
	failed := true
	defer func() {
		if failed {
			_ = lock.Close()
		}
	}()
	if resume {
		ref, err = recoveryReference(dir)
		if err != nil {
			return nil, ref, err
		}
		if err := checkResumeConfig(opts, ref); err != nil {
			return nil, ref, err
		}
		// Consume settlement evidence before any launch, including failed
		// resumes. A free directory lock alone cannot authorize replacement.
		if err := os.Remove(filepath.Join(dir, "settled.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, ref, err
		}
		// Consume shutdown evidence before any launch. A crash or failed resume
		// cannot be retried by simply observing that the directory lock is free.
		ref.Stopped = false
		if err := atomicJSON(dir, "backend.json", ref); err != nil {
			return nil, ref, err
		}
		failed = false
		return lock, ref, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, ref, err
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" {
			return nil, ref, ErrRecoveryUnsupported
		}
	}
	// A startup marker also prevents silent reuse after a failure before the
	// final backend reference is published. It never contains a PID or secret.
	if err := atomicJSON(dir, "opening.json", map[string]int{"version": 1}); err != nil {
		return nil, ref, err
	}
	for _, name := range []string{"home", "agents"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			return nil, ref, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "home", "AGENTS.md"), []byte(opts.Instructions), 0600); err != nil {
		return nil, ref, err
	}
	if opts.probe != nil {
		if err := os.WriteFile(filepath.Join(dir, "probe.mjs"), probeTool, 0600); err != nil {
			return nil, ref, err
		}
	}
	if err := atomicJSON(filepath.Join(dir, "home"), "cordis.patch.yml", profile(*opts)); err != nil {
		return nil, ref, err
	}
	failed = false
	return lock, ref, nil
}

func profile(opts Options) []map[string]any {
	patches := []map[string]any{
		{"id": "acp", "config": map[string]any{"provider": provider(opts.Model), "model": opts.Model.ModelID}},
		{"id": "approval", "config": map[string]any{"policy": "ask"}},
		{"id": "sandbox-policy", "config": map[string]any{"mode": "workspace-write"}},
	}
	if opts.probe != nil {
		patches = append(patches,
			map[string]any{"id": "acp", "inject": []string{"acpAppStartup", "teamxProbeReady"}, "config": map[string]any{"provider": provider(opts.Model), "model": opts.Model.ModelID}},
			map[string]any{"insert": []map[string]any{{"id": "teamx-probe", "name": filepath.Join(opts.StateDir, "probe.mjs"), "config": map[string]any{"receipt": opts.probe.receipt}}}},
		)
	}
	if opts.Model.Protocol == "deepseek-messages" {
		config := map[string]any{"apiKeyEnv": opts.Model.APIKeyEnv}
		if opts.Model.ReasoningEffort != "" {
			config["reasoningEffort"] = opts.Model.ReasoningEffort
		}
		if opts.Model.MaxTokens != 0 {
			config["maxTokens"] = opts.Model.MaxTokens
		}
		if opts.Model.BaseURL != "" {
			config["baseURL"] = opts.Model.BaseURL
		}
		return append(patches, map[string]any{"id": "llm-deepseek", "config": config})
	}
	contextWindow, maxTokens := opts.Model.tokenLimits()
	model := map[string]any{"id": opts.Model.ModelID, "contextWindow": contextWindow, "maxTokens": maxTokens}
	if len(opts.Model.InputModalities) > 0 {
		model["input"] = slices.Clone(opts.Model.InputModalities)
	}
	route := map[string]any{
		"apiKeyEnv": opts.Model.APIKeyEnv, "api": "openai-completions", "baseURL": opts.Model.BaseURL,
		"models": []map[string]any{model}, "retryPolicy": map[string]any{"mode": "normal", "maxRetries": 0},
	}
	if effort := opts.Model.ReasoningEffort; effort != "" {
		route["reasoning"] = effort
		// DSH requires at least one non-off level even when off is selected.
		efforts := map[string]any{"off": nil, "high": "high"}
		if effort != "off" {
			efforts[effort] = effort
		}
		model["reasoningEfforts"] = efforts
		if opts.Model.deepseek() {
			// DeepSeek gateways may think by default: omitting reasoning_effort
			// alone does not disable thinking. Keep their system-role dialect.
			model["compat"] = map[string]any{"thinkingFormat": "deepseek", "supportsDeveloperRole": false}
		}
	}
	if flag := opts.Model.Compat.SupportsDeveloperRole; flag != nil {
		compat, _ := model["compat"].(map[string]any)
		if compat == nil {
			compat = map[string]any{}
		}
		compat["supportsDeveloperRole"] = *flag
		model["compat"] = compat
	}
	return append(patches,
		map[string]any{"id": "llm-deepseek", "disabled": true},
		map[string]any{"id": "llm-pi-ai", "config": map[string]any{"providers": map[string]any{
			provider(opts.Model): route,
		}}},
	)
}

func provider(model ModelConfig) string {
	if model.Protocol == "deepseek-messages" {
		return "deepseek-official"
	}
	return "teamx"
}

func atomicJSON(dir, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func executableDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type configOption struct {
	ID           string `json:"id"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Group   string `json:"group"`
		Options []struct {
			Value string `json:"value"`
		} `json:"options"`
	} `json:"options"`
}

func modelValue(options []configOption, model ModelConfig) (string, error) {
	for _, option := range options {
		if option.ID != "model" {
			continue
		}
		for _, group := range option.Options {
			if group.Group != provider(model) {
				continue
			}
			for _, row := range group.Options {
				var pair []string
				if json.Unmarshal([]byte(row.Value), &pair) == nil && len(pair) == 2 && pair[0] == provider(model) && pair[1] == model.ModelID {
					return row.Value, nil
				}
			}
		}
	}
	return "", fmt.Errorf("configured DSH model is not advertised")
}
