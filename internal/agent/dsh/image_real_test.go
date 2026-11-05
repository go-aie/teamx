package dsh

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/testutil"
)

// TestRealDSHImage uses synthetic, independently generated pixels, never a
// GitLab notification. The random answer is absent from instructions/prompt.
func TestRealDSHImage(t *testing.T) {
	if os.Getenv("TEAMX_DSH_IMAGE_REAL") != "1" {
		t.Skip("set TEAMX_DSH_IMAGE_REAL=1 for real DSH/model image acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute evidence directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339), "component": "Go internal/agent/dsh", "scope": "I1 independent image; no GitLab or Room notification input", "recoveryStatus": "not requested; set TEAMX_DSH_IMAGE_RESUME=1 for additional recovery check"}
	defer func() {
		if err := atomicJSON(output, "report.json", report); err != nil {
			t.Error(err)
		}
		t.Logf("image acceptance report: %s", filepath.Join(output, "report.json"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	installation, err := testutil.InstalledDSH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report["installation"] = installation
	keyEnv := os.Getenv("TEAMX_DSH_API_KEY_ENV")
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	if !envName.MatchString(keyEnv) {
		t.Fatal("invalid credential environment name")
	}
	if file := os.Getenv("TEAMX_DSH_ENV_FILE"); file != "" && os.Getenv(keyEnv) == "" {
		credentialCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		key, err := exec.CommandContext(credentialCtx, "node", "--env-file="+file, "-e", "process.stdout.write(process.env[process.argv[1]] || '')", keyEnv).Output()
		if err != nil {
			t.Fatal("cannot load credential file")
		}
		t.Setenv(keyEnv, string(key))
	}
	baseURL, model := os.Getenv("MODEL_BASE_URL"), os.Getenv("MODEL_ID")
	if baseURL == "" || model == "" {
		t.Fatal("MODEL_BASE_URL and MODEL_ID required for real image acceptance")
	}
	workspace := filepath.Join(output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	challenge, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf("%08d", challenge)
	data := imageChallenge(t, code)
	if err := os.WriteFile(filepath.Join(output, "challenge.png"), data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	report["image"] = map[string]any{"count": 1, "bytes": len(data), "sha256": hex.EncodeToString(digest[:]), "expectedCode": code, "upperLeft": "red square", "lowerRight": "blue rectangle"}
	opts := Options{BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs, Workspace: workspace, StateDir: filepath.Join(output, "state"),
		Instructions: "You are an image acceptance probe. Answer using the supplied image pixels and conversation only. Never use tools, delegate, inspect files or access the network.",
		Model:        ModelConfig{Protocol: "openai-completions", BaseURL: baseURL, ModelID: model, APIKeyEnv: keyEnv, MaxTokens: 8192, InputModalities: []string{"text", "image"}}}
	report["model"] = opts.Model
	if err := acceptRealImage(ctx, opts, report, data, code); err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	report["status"] = "passed"
}

func acceptRealImage(ctx context.Context, opts Options, report map[string]any, data []byte, code string) (runErr error) {
	s, err := Open(ctx, opts)
	if err != nil {
		return err
	}
	report["backend"], report["promptCapabilities.image"] = s.reference, s.supportsImage
	defer func() {
		closeErr := s.Close(context.Background())
		report["closeSucceeded"] = closeErr == nil
		runErr = errors.Join(runErr, closeErr)
	}()
	prompt := "Read the eight-digit code in the center of the image. Identify the color and shape at the upper left and lower right. Reply only with JSON: {\"code\":\"...\",\"upper_left\":\"color shape\",\"lower_right\":\"color shape\"}. Do not use tools."
	result, err := s.RunPrompt(ctx, []ContentBlock{{Type: "text", Text: prompt}, {Type: "image", MIMEType: "image/png", Data: data}})
	report["first"] = result
	if err != nil {
		return err
	}
	var answer struct {
		Code       string `json:"code"`
		UpperLeft  string `json:"upper_left"`
		LowerRight string `json:"lower_right"`
	}
	text := strings.TrimSpace(result.Text)
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(text, "```json"), "```"), "```"))
	if json.Unmarshal([]byte(text), &answer) != nil || answer.Code != code ||
		!strings.Contains(strings.ToLower(answer.UpperLeft), "red") || !strings.Contains(strings.ToLower(answer.UpperLeft), "square") ||
		!strings.Contains(strings.ToLower(answer.LowerRight), "blue") || !strings.Contains(strings.ToLower(answer.LowerRight), "rectangle") || len(result.Tools) != 0 {
		return errors.New("pixel-only code and layout were not independently demonstrated without tools")
	}
	report["pixelsMatched"] = true
	if os.Getenv("TEAMX_DSH_IMAGE_RESUME") != "1" {
		return nil
	}
	report["recoveryStatus"] = "failed"
	if err := s.Close(context.Background()); err != nil {
		return err
	}
	resumed, err := Resume(ctx, opts)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := resumed.Close(context.Background())
		report["resumedCloseSucceeded"] = closeErr == nil
		runErr = errors.Join(runErr, closeErr)
	}()
	report["resumedSameSession"] = resumed.id == s.id
	recalled, err := resumed.Run(ctx, "Without using tools, repeat the eight-digit code from the preceding image and identify its background color. Reply only with JSON: {\"code\":\"...\",\"background\":\"...\"}.")
	report["afterResume"] = recalled
	if err != nil {
		return err
	}
	if resumed.id != s.id || !strings.Contains(recalled.Text, code) || !strings.Contains(strings.ToLower(recalled.Text), "white") || len(recalled.Tools) != 0 {
		return errors.New("original image-session context not demonstrated after normal close/resume")
	}
	report["recoveryStatus"] = "passed"
	return nil
}

// Draw a large bitmap font without embedding text metadata or a machine-readable
// answer in the image. The file is retained outside the agent workspace.
func imageChallenge(t *testing.T, code string) []byte {
	t.Helper()
	glyphs := map[rune]string{
		'0': "01110100011000110001100011000101110", '1': "00100011000010000100001000010001110",
		'2': "01110100010000100010001000100011111", '3': "11110000010000101110000010000111110",
		'4': "00010001100101010010111110001000010", '5': "11111100001000011110000010000111110",
		'6': "01110100001000011110100011000101110", '7': "11111000010001000100010000100001000",
		'8': "01110100011000101110100011000101110", '9': "01110100011000101111000010000101110",
	}
	m := image.NewRGBA(image.Rect(0, 0, 800, 500))
	draw.Draw(m, m.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(m, image.Rect(45, 35, 145, 135), image.NewUniform(color.RGBA{R: 255, A: 255}), image.Point{}, draw.Src)
	draw.Draw(m, image.Rect(570, 360, 750, 440), image.NewUniform(color.RGBA{B: 255, A: 255}), image.Point{}, draw.Src)
	const scale = 12
	for i, char := range code {
		for j, pixel := range glyphs[char] {
			if pixel == '1' {
				x, y := 118+i*6*scale+(j%5)*scale, 200+(j/5)*scale
				draw.Draw(m, image.Rect(x, y, x+scale, y+scale), image.NewUniform(color.Black), image.Point{}, draw.Src)
			}
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, m); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
