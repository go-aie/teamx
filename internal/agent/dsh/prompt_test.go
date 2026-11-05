package dsh

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func imageFixtureOptions(t *testing.T) Options {
	opts := fixtureOptions(t)
	opts.Model.InputModalities = []string{"text", "image"}
	opts.ToolEnv["TEAMX_DSH_IMAGE"] = "1"
	return opts
}

func TestInputModalitiesValidation(t *testing.T) {
	for _, protocol := range []string{"openai-completions", "deepseek-messages"} {
		for _, modalities := range [][]string{nil, {}, {"text"}, {"text", "image"}, {"image", "text"}, {"image"}, {"text", "audio"}, {"text", "text"}, {"text", ""}} {
			opts := fixtureOptions(t)
			opts.Model.Protocol, opts.Model.InputModalities = protocol, modalities
			want := len(modalities) == 0 || reflect.DeepEqual(modalities, []string{"text"}) ||
				(protocol == "openai-completions" && len(modalities) == 2 && modalities[0] != modalities[1] && (modalities[0] == "image" || modalities[1] == "image"))
			if err := CheckConfig(opts); (err == nil) != want {
				t.Fatalf("%s %v: %v", protocol, modalities, err)
			}
		}
	}
}

func TestImageCapabilityRequiredBeforeSessionCreation(t *testing.T) {
	for _, advertised := range []bool{false, true} {
		opts := imageFixtureOptions(t)
		if !advertised {
			delete(opts.ToolEnv, "TEAMX_DSH_IMAGE")
		}
		s, err := Open(context.Background(), opts)
		if !advertised {
			if s != nil || err == nil || !strings.Contains(err.Error(), "promptCapabilities.image") {
				t.Fatal("missing capability accepted", err)
			}
			if _, err := os.Stat(filepath.Join(opts.Workspace, "session-new.called")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("session created before capability check", err)
			}
			continue
		}
		if err != nil || !s.SupportsImages() {
			t.Fatal("advertised capability lost", err)
		}
		if err := s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMixedPromptOrderAndWireEncoding(t *testing.T) {
	opts := imageFixtureOptions(t)
	opts.Limits.InputBytes = 11 // Image bytes must not consume the text budget.
	s, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	data := testPNG(t)
	prompt := []ContentBlock{{Type: "text", Text: "before"}, {Type: "image", MIMEType: "image/png", Data: data}, {Type: "text", Text: "after"}, {Type: "text"}}
	if _, err := s.RunPrompt(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile(filepath.Join(opts.Workspace, "prompt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var received struct {
		Prompt []map[string]string `json:"prompt"`
	}
	if err := json.Unmarshal(wire, &received); err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{{"type": "text", "text": "before"}, {"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(data)}, {"type": "text", "text": "after"}, {"type": "text", "text": ""}}
	if !reflect.DeepEqual(received.Prompt, want) {
		t.Fatal("ACP block order or encoding changed")
	}
	// Open retained a copy; changing caller configuration cannot enable/disable it.
	opts.Model.InputModalities[1] = "text"
	if !s.SupportsImages() {
		t.Fatal("caller mutated session configuration")
	}
}

func TestPromptValidationDoesNotDispatchOrDisableSession(t *testing.T) {
	data := testPNG(t)
	imageBlock := ContentBlock{Type: "image", MIMEType: "image/png", Data: data}
	for _, kind := range []string{"empty", "unknown", "utf8", "text-total", "image-empty", "mime", "mismatch", "mixed-fields", "image-total", "text-only", "frame", "escaped-frame"} {
		t.Run(kind, func(t *testing.T) {
			opts := imageFixtureOptions(t)
			prompt := []ContentBlock{{Type: "text", Text: "before"}, imageBlock}
			switch kind {
			case "empty":
				prompt = nil
			case "unknown":
				prompt[0].Type = "resource_link"
			case "utf8":
				prompt[0].Text = string([]byte{0xff})
			case "text-total":
				opts.Limits.InputBytes = 5
				opts.Instructions = "role"
				prompt = []ContentBlock{{Type: "text", Text: "123"}, {Type: "text", Text: "456"}}
			case "image-empty":
				prompt[1].Data = nil
			case "mime":
				prompt[1].MIMEType = "image/svg+xml"
			case "mismatch":
				prompt[1].MIMEType = "image/jpeg"
			case "mixed-fields":
				prompt[1].Text = "hidden"
			case "image-total":
				opts.Limits.ImageBytes = len(data)*2 - 1
				prompt = append(prompt, imageBlock)
			case "text-only":
				opts.Model.InputModalities = nil // Even if the server advertises image.
			case "frame":
				// Raw image fits, but base64 plus the JSON envelope exceeds the frame.
				opts.Limits.FrameBytes = 8192
				prompt[1].Data = append(append([]byte(nil), data...), make([]byte, 6200)...)
			case "escaped-frame":
				prompt[0].Text = strings.Repeat("\x00", 1400)
			}
			s, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			if _, err := s.RunPrompt(context.Background(), prompt); err == nil || errors.Is(err, ErrUncertain) {
				t.Fatal("invalid prompt dispatched or reported uncertain", err)
			}
			if _, err := os.Stat(filepath.Join(opts.Workspace, "prompt.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid prompt reached server", err)
			}
			if _, err := s.Run(context.Background(), "ok"); err != nil {
				t.Fatal("validation disabled healthy session", err)
			}
		})
	}
}

func TestModalitiesMaterializedAndRecoveryBound(t *testing.T) {
	opts := imageFixtureOptions(t)
	s, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(opts.StateDir, "home", "cordis.patch.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var patches []struct {
		ID     string `json:"id"`
		Config struct {
			Provider  string `json:"provider"`
			Model     string `json:"model"`
			Providers map[string]struct {
				DefaultInput []string `json:"defaultInput"`
				Models       []struct {
					ID    string   `json:"id"`
					Input []string `json:"input"`
				} `json:"models"`
			} `json:"providers"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &patches); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range patches {
		if p.ID == "acp" && (p.Config.Provider != "teamx" || p.Config.Model != opts.Model.ModelID) {
			t.Fatal("initialize route was not configured")
		}
		if p.ID == "llm-pi-ai" {
			route := p.Config.Providers["teamx"]
			found = len(route.Models) == 1 && route.Models[0].ID == opts.Model.ModelID && reflect.DeepEqual(route.Models[0].Input, opts.Model.InputModalities) && route.DefaultInput == nil
		}
	}
	if !found {
		t.Fatal("modalities not declared on exact model")
	}
	changed := opts
	changed.Model.InputModalities = nil
	if err := CheckResumeConfig(changed); !errors.Is(err, ErrRecoveryUnsupported) {
		t.Fatal("changed modalities allowed recovery", err)
	}
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.SupportsImages() || resumed.id != s.id {
		t.Fatal("image capability or original session lost on resume")
	}
	if err := resumed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
