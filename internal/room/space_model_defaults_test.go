package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestPersonalSpaceInitialModelUsesSelectedSourceAndIndependentCredentials(t *testing.T) {
	seed := resetSeedForTest(t)
	s, endpoint, _ := startArchivableSpaceServerFixture(t, t.TempDir(), func(r *SpaceRuntime) { r.config.resetTeamsSource = seed })
	a := s.initial.SpaceID
	b := strings.Repeat("b", 32)
	input := spaceChangeInput{RequestID: b, Name: "Work", ModelSourceSpaceID: a}
	var created SpaceRecord
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", input, &created); status != 202 {
		t.Fatal(status)
	}
	first := waitSpaceState(t, s, b, "ready")
	_ = waitSpaceAssistant(t, first)
	firstModel, _ := first.manager.config.models.catalog().resolve(modelSelection{Mode: "default"})
	source := waitSpaceState(t, s, a, "ready")
	sourceModel, _ := source.manager.config.models.catalog().resolve(modelSelection{Mode: "default"})
	if !reflect.DeepEqual(firstModel.ModelParameters, sourceModel.ModelParameters) || firstModel.CredentialRef == sourceModel.CredentialRef || !first.manager.config.models.hasValidation(firstModel.ID) {
		t.Fatal("initial model skipped validation or shared a credential reference")
	}
	// The current Space can differ from the installation's default Space.
	// Configure a separate source through ordinary first use, without coupling
	// this bootstrap check to online Session rotation acceptance.
	d := strings.Repeat("d", 32)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", spaceChangeInput{RequestID: d, Name: "Custom source"}, nil); status != 202 {
		t.Fatal(status)
	}
	waitSpaceState(t, s, d, "setup_required")
	custom := firstModel.clone()
	custom.ModelID = "custom-work-model"
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(d)+"/v1/setup/model", setupModelInput{RequestID: "custom-source", ExpectedVersion: 1, ModelParameters: custom.ModelParameters, APIKey: "private-work-key"}, nil); status != 202 {
		t.Fatal(status)
	}
	selectedSource := waitSpaceState(t, s, d, "ready")
	var probes atomic.Int32
	s.assembly.config.modelProbe = func(ctx context.Context, opts dsh.Options) (dsh.ProbeResult, error) {
		probes.Add(1)
		if opts.Credential == nil || opts.Credential.Value != "private-work-key" || opts.Model.ModelID != custom.ModelID {
			t.Error("new Space did not use its selected source")
		}
		return successfulModelProbe(ctx, opts)
	}
	c := strings.Repeat("c", 32)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", spaceChangeInput{RequestID: c, Name: "Project", ModelSourceSpaceID: d}, nil); status != 202 {
		t.Fatal(status)
	}
	clone := waitSpaceState(t, s, c, "ready")
	entry, _ := clone.manager.config.models.catalog().resolve(modelSelection{Mode: "default"})
	key, err := clone.manager.config.models.Credentials.get(entry.CredentialRef)
	if err != nil || key.Value != "private-work-key" || entry.ModelID != custom.ModelID || probes.Load() != 1 {
		t.Fatal("automatic setup was not validated once", err, probes.Load())
	}
	if _, err := selectedSource.manager.config.models.Credentials.get(entry.CredentialRef); err == nil {
		t.Fatal("credential reference crosses Space stores")
	}
	// Retrying creation returns the same Space without another model call.
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", spaceChangeInput{RequestID: c, Name: "Project", ModelSourceSpaceID: d}, nil); status != 202 || probes.Load() != 1 {
		t.Fatal(status, probes.Load())
	}
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", spaceChangeInput{RequestID: c, Name: "Project", ModelSourceSpaceID: a}, nil); status != 409 {
		t.Fatal("retry changed initial model source", status)
	}
	for _, runtime := range []*SpaceRuntime{first, selectedSource, clone} {
		data, err := os.ReadFile(filepath.Join(runtime.roots.Home, "config.toml"))
		if err != nil || strings.Contains(string(data), "private-work-key") {
			t.Fatal("key escaped the private credential store", err)
		}
	}
	// Reset returns to first use instead of importing the source model again.
	record := spaceRecordForTest(t, s, c)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+c+"/reset", resetInputForTest(record), nil); status != 202 {
		t.Fatal(status)
	}
	waitResetForTest(t, s, c)
	waitSpaceState(t, s, c, "setup_required")
	var state struct {
		CredentialConfigured bool            `json:"credential_configured"`
		Model                ModelParameters `json:"model"`
	}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(c)+"/v1/setup", nil, &state); status != 200 || state.CredentialConfigured || state.Model.ModelID != "" || probes.Load() != 1 {
		t.Fatal("reset imported model settings again", status, state, probes.Load())
	}
}

func TestPersonalSpaceInitialModelFailureSurvivesRestartAndAllowsRetry(t *testing.T) {
	home := t.TempDir()
	s, endpoint, stop := startArchivableSpaceServerFixture(t, home)
	var probes atomic.Int32
	s.assembly.config.modelProbe = func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		probes.Add(1)
		return dsh.ProbeResult{}, errors.New("controlled failure")
	}
	id := strings.Repeat("b", 32)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", spaceChangeInput{RequestID: id, Name: "Retry", ModelSourceSpaceID: s.initial.SpaceID}, nil); status != 202 {
		t.Fatal(status)
	}
	waitSpaceState(t, s, id, "setup_required")
	var state struct {
		Version              uint64          `json:"version"`
		Model                ModelParameters `json:"model"`
		CredentialConfigured bool            `json:"credential_configured"`
		Operation            *setupOperation `json:"operation"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup", nil, &state)
		if state.Operation != nil && state.Operation.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic failure was not visible")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if probes.Load() != 1 || !state.CredentialConfigured || state.Model.ModelID == "" {
		t.Fatal(state, probes.Load())
	}
	stop()
	var restartedProbes atomic.Int32
	restarted, next, _ := startSpaceServerFixture(t, home, func(r *SpaceRuntime) {
		r.config.modelProbe = func(ctx context.Context, o dsh.Options) (dsh.ProbeResult, error) {
			restartedProbes.Add(1)
			return successfulModelProbe(ctx, o)
		}
	})
	waitSpaceState(t, restarted, id, "setup_required")
	spaceHTTPRequest(t, next, spaceRoute(id)+"/v1/setup", nil, &state)
	if restartedProbes.Load() != 0 || state.Operation == nil || state.Operation.Status != "failed" || !state.CredentialConfigured {
		t.Fatal("restart replayed failed automatic setup", state, restartedProbes.Load())
	}
	if status := spaceHTTPRequest(t, next, spaceRoute(id)+"/v1/setup/model", setupModelInput{RequestID: "retry-model", ExpectedVersion: state.Version, ModelParameters: state.Model}, nil); status != 202 {
		t.Fatal(status)
	}
	waitSpaceState(t, restarted, id, "ready")
	if restartedProbes.Load() != 1 {
		t.Fatal("explicit retry did not use ordinary model validation", restartedProbes.Load())
	}
}

func TestPersonalSpaceInitialModelMissingSourceFallsBackAndForeignSourceIsRejected(t *testing.T) {
	s, endpoint, _ := startSpaceServerFixture(t, t.TempDir())
	id := strings.Repeat("b", 32)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", spaceChangeInput{RequestID: id, Name: "Manual", ModelSourceSpaceID: s.initial.SpaceID}, nil); status != 202 {
		t.Fatal(status)
	}
	waitSpaceState(t, s, id, "setup_required")
	var state struct {
		CredentialConfigured bool            `json:"credential_configured"`
		Operation            *setupOperation `json:"operation"`
	}
	spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup", nil, &state)
	if state.CredentialConfigured || state.Operation != nil {
		t.Fatal("unconfigured source should show ordinary first use", state)
	}
	for _, source := range []string{strings.Repeat("e", 32), strings.Repeat("c", 32), "unsafe"} {
		if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", spaceChangeInput{RequestID: strings.Repeat("c", 32), Name: "Denied", ModelSourceSpaceID: source}, nil); status != 403 {
			t.Fatal("unowned/self/invalid source admitted", source, status)
		}
	}
	data, _ := json.Marshal(spaceRecordForTest(t, s, id))
	if strings.Contains(string(data), "api_key") {
		t.Fatal("creation response exposes credentials")
	}
}

func TestPersonalSpaceInitialModelMissingCredentialShowsPrefilledSetup(t *testing.T) {
	s, endpoint, _ := startArchivableSpaceServerFixture(t, t.TempDir())
	source := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	entry, _ := source.manager.config.models.catalog().resolve(modelSelection{Mode: "default"})
	if err := os.Remove(filepath.Join(source.roots.Home, "model-credentials", entry.CredentialRef)); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("b", 32)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", spaceChangeInput{RequestID: id, Name: "Missing key", ModelSourceSpaceID: source.SpaceID}, nil); status != 202 {
		t.Fatal(status)
	}
	waitSpaceState(t, s, id, "setup_required")
	var state struct {
		Model                ModelParameters `json:"model"`
		CredentialConfigured bool            `json:"credential_configured"`
		Operation            *setupOperation `json:"operation"`
	}
	spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup", nil, &state)
	if state.CredentialConfigured || state.Operation != nil || !reflect.DeepEqual(state.Model, entry.ModelParameters) {
		t.Fatal("missing key did not preserve editable model parameters", state)
	}
}
