package dsh

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/agent"
)

// The real SRT cache is tested in its package. This wrapper exercises the
// protocol adapter's optional cache seam with real owned stdio subprocesses.
type versionCacheExecution struct {
	alternateExecution
	version string
}

func (f *versionCacheExecution) RuntimeVersion(ctx context.Context, _ agent.ExecutionRequest, query func(context.Context) (string, error)) (string, error) {
	if f.version != "" {
		return f.version, nil
	}
	version, err := query(ctx)
	if err == nil {
		f.version = version
	}
	return version, err
}

func TestVersionReusePreservesACPAndSessionRecovery(t *testing.T) {
	opts := fixtureOptions(t)
	opts.Artifact = Artifact{}
	factory := &versionCacheExecution{alternateExecution: alternateExecution{native: nativeExecution{inspect: opts.inspect}}}
	opts.Execution = factory
	identity, err := InspectRuntime(t.Context(), opts)
	if err != nil || identity.Version != "test" {
		t.Fatal("runtime inspection", identity, err)
	}
	s := openFixture(t, opts)
	if _, err := s.Run(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := CheckResumeConfig(opts); err != nil {
		t.Fatal(err)
	}
	resumed, err := Resume(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(context.Background())
	if resumed.id != s.id {
		t.Fatal("version reuse replaced the original Session")
	}
	if _, err := resumed.Run(t.Context(), "again"); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	versions, sessions := 0, 0
	for _, request := range factory.requests {
		switch request.Args[len(request.Args)-1] {
		case "--version":
			versions++
		case "acp":
			sessions++
		}
	}
	if versions != 1 || sessions != 2 {
		t.Fatal("version reuse changed ACP acquisition", versions, sessions)
	}
}

func TestVersionCachePreservesPartialProcessOwnership(t *testing.T) {
	opts := fixtureOptions(t)
	opts.Artifact = Artifact{}
	opts.Limits.CleanupTimeout = 30 * time.Millisecond
	gate := make(chan struct{})
	factory := &versionCacheExecution{alternateExecution: alternateExecution{native: nativeExecution{inspect: opts.inspect}, stopGate: gate}}
	opts.Execution = factory
	s, err := Open(t.Context(), opts)
	if s == nil || !errors.Is(err, ErrCleanupPending) || factory.version != "" {
		close(gate)
		t.Fatal("version cache lost partial ownership or cached failure", s, err)
	}
	if lock, err := lockDirectory(opts.StateDir); !errors.Is(err, ErrLocked) {
		if lock != nil {
			lock.Close()
		}
		close(gate)
		t.Fatal("version cleanup released the Session lock", err)
	}
	close(gate)
	if err := s.Close(t.Context()); errors.Is(err, ErrCleanupPending) {
		t.Fatal("version process did not settle", err)
	}
	for _, request := range factory.requests {
		if strings.Join(request.Args, " ") == strings.Join(opts.BinaryArgs, " ")+" --profile acp" {
			t.Fatal("failed version query started ACP")
		}
	}
}
