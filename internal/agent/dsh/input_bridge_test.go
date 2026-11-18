package dsh

import (
	"context"
	"os"
	"os/exec"
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
