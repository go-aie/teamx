package dsh

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProbeVersion records the managed installation used for acceptance. Probe
// checks compatibility through isolated execution rather than version equality.
const ProbeVersion = "0.2.2-rc.1"

var probeSlot = make(chan struct{}, 1)

//go:embed probe_tool.mjs
var probeTool []byte

type probeOptions struct{ receipt string }

type ProbeResult struct {
	Identity      RuntimeIdentity `json:"identity"`
	Text          bool            `json:"text"`
	ToolRoundTrip bool            `json:"tool_round_trip"`
	Cleanup       bool            `json:"cleanup"`
	// Image support is advertised, not exercised by this text/tool probe.
	ImageAdvertised bool `json:"image_advertised"`
}

type ProbeError struct{ Stage, Code string }

func (e *ProbeError) Error() string {
	return "model validation failed: " + e.Stage + " (" + e.Code + ")"
}

func probeFailure(stage string, err error) error {
	code := "tool_acp"
	var status *HTTPStatusError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, context.Canceled):
		code = "cancelled"
	case errors.Is(err, ErrDNS):
		code = "dns"
	case errors.Is(err, ErrConnection):
		code = "connection"
	case errors.Is(err, ErrTLS):
		code = "tls"
	case errors.Is(err, ErrUpstreamTimeout):
		code = "timeout"
	case errors.Is(err, ErrAuthentication):
		code = "authentication"
	case errors.Is(err, ErrRateLimited):
		code = "rate_quota"
	case errors.Is(err, ErrModelNotFound), errors.Is(err, ErrModelUnavailable):
		code = "route_model_access"
	case errors.As(err, &status):
		switch {
		case status.StatusCode == 401 || status.StatusCode == 403:
			code = "authentication"
		case status.StatusCode == 429:
			code = "rate_quota"
		case status.StatusCode == 408 || status.StatusCode == 504:
			code = "timeout"
		case status.StatusCode >= 500:
			code = "upstream_5xx"
		case status.StatusCode == 400 || status.StatusCode == 422:
			code = "parameters_roles"
		default:
			code = "route_model_access"
		}
	}
	return &ProbeError{Stage: stage, Code: code}
}

// Probe owns an isolated home, empty workspace and a fixed tool. It accepts only
// launcher/model/credential input: no business environment, authority or prompt.
// A separate cleanup budget is reserved inside the total 90 second budget.
func Probe(ctx context.Context, launcher Options) (result ProbeResult, err error) {
	releaseSlot := true
	select {
	case probeSlot <- struct{}{}:
		defer func() {
			if releaseSlot {
				<-probeSlot
			}
		}()
	default:
		return result, &ProbeError{"admission", "busy"}
	}
	opts := Options{BinaryPath: launcher.BinaryPath, BinaryArgs: append([]string(nil), launcher.BinaryArgs...), Model: launcher.Model.Clone(), Credential: launcher.Credential}
	if err := CheckConfig(opts); err != nil {
		return result, &ProbeError{"fields", "invalid"}
	}
	if opts.Credential == nil || opts.Credential.Value == "" {
		return result, &ProbeError{"credential", "missing"}
	}
	value := *opts.Credential
	opts.Credential = &value
	total, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	work, stop := context.WithTimeout(total, 70*time.Second)
	defer stop()
	root, err := os.MkdirTemp("", "teamx-model-probe-*")
	if err != nil {
		return result, &ProbeError{"local", "private_directory"}
	}
	opts.Workspace, opts.StateDir = filepath.Join(root, "workspace"), filepath.Join(root, "state")
	if err := os.Mkdir(opts.Workspace, 0700); err != nil {
		_ = os.RemoveAll(root)
		return result, &ProbeError{"local", "private_directory"}
	}
	defer func() {
		if result.Cleanup || result.Identity.Version == "" {
			_ = os.RemoveAll(root)
		}
	}()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, &ProbeError{"local", "random"}
	}
	opts.probe = &probeOptions{receipt: "TEAMX_PROBE_" + hex.EncodeToString(nonce[:])}
	opts.Instructions = "You are a connectivity probe. Follow only the two short requests. Use no tools unless explicitly requested."
	opts.Limits = Limits{HandshakeTimeout: 15 * time.Second, TurnTimeout: 50 * time.Second, CancelTimeout: time.Second, CleanupTimeout: 8 * time.Second, TextBytes: 8192}
	identity, err := InspectRuntime(work, opts)
	if err != nil {
		return result, probeFailure("local_identity", err)
	}
	result.Identity = identity
	s, openErr := Open(work, opts)
	if s == nil && openErr != nil {
		result.Cleanup = !errors.Is(openErr, ErrCleanupPending)
	}
	if s != nil {
		defer func() {
			cleanup, done := context.WithTimeout(context.Background(), 18*time.Second)
			defer done()
			closeErr := s.Close(cleanup)
			result.Cleanup = CleanupSettled(closeErr)
			if errors.Is(closeErr, ErrCleanupPending) {
				// Preserve concurrency ownership after the caller's bounded wait.
				releaseSlot = false
				go func() {
					<-s.cleanupDone
					if CleanupSettled(s.cleanupErr) {
						_ = os.RemoveAll(root)
					}
					<-probeSlot
				}()
			}
			if closeErr != nil {
				err = errors.Join(err, &ProbeError{"cleanup", "failed"})
			}
		}()
	}
	if openErr != nil {
		return result, probeFailure("handshake", openErr)
	}
	result.ImageAdvertised = s.supportsImage
	result.Identity.Agent = s.RuntimeAgent()
	text, runErr := s.Run(work, "Reply exactly TEAMX_TEXT_OK. Do not call tools.")
	if runErr != nil {
		return result, probeFailure("text", runErr)
	}
	if !strings.Contains(text.Text, "TEAMX_TEXT_OK") || len(text.Tools) != 0 {
		return result, &ProbeError{"text", "unexpected_response"}
	}
	result.Text = true
	tool, runErr := s.Run(work, "Call teamx_probe exactly once with {}. Then reply with the exact receipt it returned. Do not call any other tool.")
	if runErr != nil {
		return result, probeFailure("tool", runErr)
	}
	completed := 0
	ids := map[string]bool{}
	for _, update := range tool.Tools {
		ids[update.ID] = true
		if update.Status == "completed" && strings.Contains(update.Output, opts.probe.receipt) {
			completed++
		}
	}

	if completed != 1 || len(ids) != 1 || !strings.Contains(tool.Text, opts.probe.receipt) {
		return result, &ProbeError{"tool", "round_trip_missing"}
	}
	result.ToolRoundTrip = true
	return result, nil
}

func probeAmbientEnv(name string) bool {
	switch name {
	case "PATH", "LANG", "LC_ALL", "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS":
		return true
	}
	return false
}
