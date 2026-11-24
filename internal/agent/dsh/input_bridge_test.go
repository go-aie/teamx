package dsh

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestInputBridgeFrameworkDurabilityAndReplay(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required to execute the DSH bridge contract test")
	}
	// Include the child test source in Go's test cache dependencies.
	if _, err := os.ReadFile("input_bridge_test.mjs"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--test", "input_bridge_test.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bridge contract: %v\n%s", err, output)
	}
}

func TestInputBridgeCredentialsAndImplementationIdentity(t *testing.T) {
	s := &Session{}
	s.AddRedactionSecret("first-input-credential")
	s.AddRedactionSecret("next-input-credential")
	if text := s.redact("first-input-credential next-input-credential"); text != "[REDACTED] [REDACTED]" {
		t.Fatal(text)
	}
	opts := Options{Instructions: "stable instructions"}
	plain := configDigest(opts)
	opts.InputBridge = true
	if plain == configDigest(opts) {
		t.Fatal("bridge missing from Session identity")
	}
	before := configDigest(opts)
	original := inputBridgePlugin
	inputBridgePlugin = append(append([]byte(nil), original...), []byte("\n// changed protocol implementation\n")...)
	defer func() { inputBridgePlugin = original }()
	if before == configDigest(opts) {
		t.Fatal("changed bridge accepted as old Session")
	}
}

func TestInputBridgeUpgradeRequiresSettledRotationForExistingPlainSession(t *testing.T) {
	opts := fixtureOptions(t)
	opts.InputBridge = true
	// Reproduce the former retry rule in an isolated saved Session. This member
	// has never had Computer: upgrading the bridge still changes its fingerprint.
	current := inputBridgePlugin
	legacy, err := os.ReadFile("testdata/input_bridge_legacy.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(legacy, current) {
		t.Fatal("legacy bridge fixture no longer matches the changed seam")
	}
	inputBridgePlugin = legacy
	session := openFixture(t, opts)
	inputBridgePlugin = current
	defer func() { inputBridgePlugin = current; _ = session.Close(context.Background()) }()
	if err := CheckReset(opts.StateDir); !errors.Is(err, ErrLocked) {
		t.Fatal("unsettled old Session allowed reset", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(opts.StateDir, "backend.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckResumeConfig(opts); !errors.Is(err, ErrRecoveryUnsupported) {
		t.Fatal("old retry bridge resumed silently", err)
	}
	if err = CheckReset(opts.StateDir); err != nil {
		t.Fatal("normally closed old Session cannot rotate", err)
	}
	after, err := os.ReadFile(filepath.Join(opts.StateDir, "backend.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("preflight consumed original saved Session", err)
	}
	fresh := opts
	fresh.StateDir = filepath.Join(t.TempDir(), "new-generation")
	next := openFixture(t, fresh)
	if err = next.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if next.id == session.id {
		t.Fatal("settled rotation reused private Session identity")
	}
}
