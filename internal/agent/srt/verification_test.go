package srt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-aie/teamx/agent"
)

func TestRuntimeVerificationSharedBySpacesAndKeepsAdmissionChecks(t *testing.T) {
	f, request := policyFixture(t)
	if _, err := f.Identify(request); err == nil {
		t.Fatal("unchecked deployment returned an execution identity")
	}
	var checks atomic.Int32
	verify := func() (agent.ExecutionIdentity, error) {
		checks.Add(1)
		return agent.ExecutionIdentity{Kind: "srt", Version: Version, RuntimeSHA256: "verified-release"}, nil
	}
	var work sync.WaitGroup
	for range 12 {
		work.Go(func() {
			space := f.ForSpace("user", "space").(*Factory)
			if err := space.verified.check(t.Context(), verify); err != nil {
				t.Error(err)
			}
			if id, err := space.Identify(request); err != nil || id.RuntimeSHA256 != "verified-release" || id.PolicySHA256 == "" {
				t.Error("verified identity or generation policy lost", id, err)
			}
		})
	}
	work.Wait()
	if checks.Load() != 1 {
		t.Fatal("shared runtime was verified repeatedly", checks.Load())
	}
	for _, change := range []func(*agent.ExecutionRequest){
		func(r *agent.ExecutionRequest) { r.Binding.SpaceID = "another-space" },
		func(r *agent.ExecutionRequest) { r.Endpoints = []string{"https://unauthorized.example"} },
		func(r *agent.ExecutionRequest) { r.Workspace = f.config.PrivateRoots[0] },
	} {
		changed := request
		change(&changed)
		if _, err := f.Identify(changed); err == nil {
			t.Fatal("runtime reuse bypassed execution admission")
		}
	}
	// A new service process cannot inherit another process's verification.
	if _, err := New(f.config).ForSpace("user", "space").Identify(request); err == nil {
		t.Fatal("new deployment reused previous verification")
	}
}

func TestRuntimeVerificationDoesNotPublishFailureOrCancellation(t *testing.T) {
	v := newVerifiedRuntime()
	failed := errors.New("sandbox prerequisite failed")
	if err := v.check(t.Context(), func() (agent.ExecutionIdentity, error) {
		return agent.ExecutionIdentity{Kind: "srt"}, failed
	}); !errors.Is(err, failed) || v.identity != nil {
		t.Fatal("failed startup was cached", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := v.check(ctx, func() (agent.ExecutionIdentity, error) {
		cancel()
		return agent.ExecutionIdentity{Kind: "srt"}, nil
	}); !errors.Is(err, context.Canceled) || v.identity != nil {
		t.Fatal("canceled startup was cached", err)
	}
	if err := v.check(t.Context(), func() (agent.ExecutionIdentity, error) {
		return agent.ExecutionIdentity{Kind: "srt"}, nil
	}); err != nil || v.identity == nil {
		t.Fatal("fresh successful verification unavailable", err)
	}
}

func TestRuntimeVersionSharedAcrossSpacesAndBoundToLauncher(t *testing.T) {
	f := New(Config{})
	path := filepath.Join(t.TempDir(), "dsh")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	f.config.Node.Path = path
	if err := f.verified.check(t.Context(), func() (agent.ExecutionIdentity, error) {
		return agent.ExecutionIdentity{Kind: "srt"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var queries atomic.Int32
	query := func(context.Context) (string, error) { queries.Add(1); return "fixture-version", nil }
	var work sync.WaitGroup
	for _, spaceID := range []string{"a", "b", "c", "a", "b", "c"} {
		work.Go(func() {
			space := f.ForSpace("user", spaceID).(*Factory)
			request := agent.ExecutionRequest{BinaryPath: path, Binding: agent.ExecutionBinding{UserID: "user", SpaceID: spaceID}}
			if version, err := space.RuntimeVersion(t.Context(), request, query); err != nil || version != "fixture-version" {
				t.Error(version, err)
			}
		})
	}
	work.Wait()
	if queries.Load() != 1 {
		t.Fatal("version query repeated across spaces", queries.Load())
	}
	space := f.ForSpace("user", "a").(*Factory)
	request := agent.ExecutionRequest{BinaryPath: path, Binding: agent.ExecutionBinding{UserID: "user", SpaceID: "a"}, Args: []string{"different-launcher"}}
	if _, err := space.RuntimeVersion(t.Context(), request, query); err != nil || queries.Load() != 2 {
		t.Fatal("different launcher reused a version", queries.Load(), err)
	}
	request.Args = []string{"failure"}
	for range 2 {
		if _, err := space.RuntimeVersion(t.Context(), request, func(context.Context) (string, error) {
			queries.Add(1)
			return "", errors.New("query cleanup pending")
		}); err == nil {
			t.Fatal("failed query succeeded")
		}
	}
	if queries.Load() != 4 {
		t.Fatal("failed version query was cached")
	}
	request.Binding.SpaceID = "b"
	if _, err := space.RuntimeVersion(t.Context(), request, query); err == nil || queries.Load() != 4 {
		t.Fatal("cached version bypassed Space binding")
	}
	request.Binding.SpaceID = "a"
	request.Args = []string{path + ".mjs"}
	if err := os.WriteFile(path+".mjs", []byte("mutable-script"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := space.RuntimeVersion(t.Context(), request, query); err != nil {
			t.Fatal(err)
		}
	}
	if queries.Load() != 6 {
		t.Fatal("unverified script version was cached")
	}
}

func TestRuntimeVersionWaitHonorsCancellation(t *testing.T) {
	f := New(Config{})
	f.config.Node.Path, _ = filepath.EvalSymlinks(os.Args[0])
	f.verified.identity = &agent.ExecutionIdentity{Kind: "srt"}
	space := f.ForSpace("user", "space").(*Factory)
	request := agent.ExecutionRequest{BinaryPath: f.config.Node.Path, Binding: agent.ExecutionBinding{UserID: "user", SpaceID: "space"}}
	f.verified.versionGate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := space.RuntimeVersion(ctx, request, func(context.Context) (string, error) {
		t.Fatal("canceled waiter queried version")
		return "", nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-f.verified.versionGate
}
