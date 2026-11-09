package room

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func scriptedBundlePrompt(t *testing.T, answers []string, secretReads *int, output io.Writer) *bundlePrompt {
	t.Helper()
	index := 0
	return &bundlePrompt{out: output, read: func(_ string, fallback string, secret bool) (string, error) {
		if index == len(answers) {
			return "", io.EOF
		}
		answer := answers[index]
		index++
		if secret {
			(*secretReads)++
		} else if answer == "" {
			answer = fallback
		}
		return answer, nil
	}}
}

func unsetBundleCredential(t *testing.T, name string) {
	t.Helper()
	old, exists := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(name, old)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func TestBundleInteractiveInitSavesPrivateConfiguration(t *testing.T) {
	const name = "TEAMX_BUNDLE_INIT_TEST_KEY"
	const secret = "private-'quoted'\\key\"-do-not-print"
	unsetBundleCredential(t, name)
	var output bytes.Buffer
	var secretReads int
	p := scriptedBundlePrompt(t, []string{"", "https://model.invalid/v1", "model-'quoted'", name, "", secret}, &secretReads, &output)
	setup, err := promptBundleModel(p)
	if err != nil {
		t.Fatal(err)
	}
	root := bundleTestRoot(t)
	home := filepath.Join(root, ".teamx")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := initializeBundleWithModel(root, home, setup); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c startupConfig
	if err := toml.Unmarshal(data, &c); err != nil {
		t.Fatal("generated config is invalid")
	}
	if !reflect.DeepEqual(c.Runtime.Model, setup.Model) || c.Env[name] != secret || secretReads != 2 {
		t.Fatal("model settings or credential not preserved")
	}
	if _, err := loadStartupConfig(path, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("configuration is not private", err)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatal("credential leaked to output")
	}
	// Even an interactive init leaves an existing configuration byte-for-byte intact.
	if err := runBundle("init", []string{"--bundle-root", root}, &output, &output); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("reinitialization changed user configuration", err)
	}
}

func TestBundleInteractiveInitValidatesAndUsesExportedCredential(t *testing.T) {
	const name = "TEAMX_BUNDLE_INIT_TEST_KEY"
	t.Setenv(name, "exported-private-key-do-not-save")
	var output bytes.Buffer
	var secretReads int
	p := scriptedBundlePrompt(t, []string{
		"unsupported", "openai-completions",
		"", "https://user:password@model.invalid/v1", "https://model.invalid/v1",
		"", "test-model",
		"PATH", "TEAMX_CLI", "TEAMX_ROOM_ID", "invalid-name", name,
	}, &secretReads, &output)
	setup, err := promptBundleModel(p)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Model.Protocol != "openai-completions" || setup.Model.BaseURL != "https://model.invalid/v1" || setup.Model.ModelID != "test-model" || setup.Model.APIKeyEnv != name || len(setup.Env) != 0 || secretReads != 0 {
		t.Fatal("invalid setting accepted or exported credential saved")
	}
	if strings.Contains(output.String(), "password") || strings.Contains(output.String(), "exported-private-key-do-not-save") {
		t.Fatal("sensitive value leaked")
	}
}

func TestBundleInteractiveInitCancellationAndEmptyExport(t *testing.T) {
	const name = "TEAMX_BUNDLE_INIT_TEST_KEY"
	for _, exported := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "empty export"}[exported], func(t *testing.T) {
			if exported {
				t.Setenv(name, "")
			} else {
				unsetBundleCredential(t, name)
			}
			var secretReads int
			p := scriptedBundlePrompt(t, []string{"", "https://model.invalid/v1", "test-model", name}, &secretReads, io.Discard)
			setup, err := promptBundleModel(p)
			if setup != nil || err == nil || !exported && !errors.Is(err, io.EOF) {
				t.Fatal("incomplete initialization accepted")
			}
		})
	}
}

func TestBundlePromptSkipsRedirectedInput(t *testing.T) {
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	p, err := newBundlePrompt(input, io.Discard)
	if err != nil || p != nil {
		t.Fatal("redirected stdin must not prompt", err)
	}
}
