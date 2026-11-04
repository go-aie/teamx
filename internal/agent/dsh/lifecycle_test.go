package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCloseExitDeterminesResumeEligibility(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		grace   time.Duration
		failure string
	}{
		{mode: "slow-exit", grace: 2 * time.Second},
		{mode: "stuck-exit", grace: 100 * time.Millisecond, failure: "forced cleanup"},
		{mode: "abnormal-exit", grace: time.Second, failure: "exit status 3"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			opts := fixtureOptions(t)
			opts.ToolEnv["TEAMX_DSH_MODE"] = tc.mode
			opts.Limits.CancelTimeout = tc.grace
			s, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			if tc.mode == "slow-exit" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				err := s.Close(ctx)
				cancel()
				if !errors.Is(err, ErrCleanupPending) {
					t.Fatalf("short caller wait reported cleanup complete: %v", err)
				}
				if lock, err := lockDirectory(opts.StateDir); !errors.Is(err, ErrLocked) {
					if lock != nil {
						_ = lock.Close()
					}
					t.Fatalf("released directory before natural exit: %v", err)
				}
			}
			for range 2 { // Repeated Close must preserve the final outcome.
				err := s.Close(context.Background())
				if tc.failure == "" {
					if err != nil {
						t.Fatalf("slow natural exit was escalated: %v", err)
					}
				} else if !errors.Is(err, ErrRecoveryUnsupported) || !strings.Contains(err.Error(), tc.failure) || errors.Is(err, ErrCleanupPending) {
					t.Fatalf("lost completed cleanup failure: %v", err)
				}
				if !CleanupSettled(err) {
					t.Fatalf("completed cleanup did not permit an explicit reset: %v", err)
				}
			}
			if live, err := opts.inspect(); err != nil || len(live) != 0 {
				t.Fatal("Close returned before process exit", live, err)
			}
			if err := CheckResume(opts.StateDir); (err == nil) != (tc.failure == "") {
				t.Fatalf("incorrect recovery permission: %v", err)
			}
			if err := CheckReset(opts.StateDir); err != nil {
				t.Fatalf("settled execution blocked explicit reset: %v", err)
			}
		})
	}
}

func TestCancellationDuringPermissionDoesNotBlockReader(t *testing.T) {
	opts := fixtureOptions(t)
	entered := make(chan struct{})
	opts.Permission = func(ctx context.Context, _ PermissionRequest) (string, error) {
		close(entered)
		<-ctx.Done()
		return "allow", nil
	}
	s := openFixture(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Run(ctx, "permission-hold"); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCancelled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("permission blocked cancellation/reader")
	}
	if _, err := s.Run(context.Background(), "after"); err != nil {
		t.Fatal(err)
	}
}

func TestReverseRequestsAreBounded(t *testing.T) {
	c, _, _ := pipeConnection(t)
	s := &Session{conn: c, reverseSlots: make(chan struct{}, 4), reverseIDs: map[string]bool{}}
	if err := s.opts.Limits.defaults(); err != nil {
		t.Fatal(err)
	}
	<-c.writeSlot // all reverse writes now wait, while the reader stays free
	for id := 1; id <= 4; id++ {
		if err := s.request(frame{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(id)), Method: "unknown"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.request(frame{JSONRPC: "2.0", ID: json.RawMessage("5"), Method: "unknown"}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("unbounded reverse work: %v", err)
	}
	c.fail(ErrUnavailable)
	c.writeSlot <- struct{}{}
	s.workers.Wait()
}

func TestCloseProtocolFailureStillReapsAndReleases(t *testing.T) {
	opts := fixtureOptions(t)
	opts.ToolEnv["TEAMX_DSH_MODE"] = "stuck-close"
	s, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCleanupPending) {
		t.Fatalf("lost close error: %v", err)
	}
	select {
	case <-s.process.waitDone:
	default:
		t.Fatal("close returned before Wait")
	}
	lock, err := lockDirectory(opts.StateDir)
	if err != nil {
		t.Fatalf("completed cleanup retained lock: %v", err)
	}
	_ = lock.Close()
	if err := s.Close(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("idempotent close lost error: %v", err)
	}
}

func TestCancelledTerminalRetainsSlotUntilToolExit(t *testing.T) {
	for _, input := range []string{"cancel-settle-tool", "cancel-live-tool"} {
		t.Run(input, func(t *testing.T) {
			opts := fixtureOptions(t)
			opts.Limits.CancelTimeout = time.Second
			s := openFixture(t, opts)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := s.Run(ctx, input); done <- err }()
			waitFor(t, func() bool {
				live, err := opts.inspect()
				return err == nil && len(live) == 2
			})
			cancel()
			// The fixture sends cancelled before its tool exits. Observe that
			// terminal, then exercise the still-occupied execution slot.
			waitFor(t, func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.active != nil && s.active.result.StopReason == "cancelled"
			})
			if _, err := s.Run(context.Background(), "overlap"); !errors.Is(err, ErrBusy) {
				t.Fatalf("cancel terminal released slot before tool exit: %v", err)
			}
			if input == "cancel-settle-tool" {
				if err := os.WriteFile(filepath.Join(opts.Workspace, "tool.release"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := <-done
			if input == "cancel-settle-tool" {
				if !errors.Is(err, ErrCancelled) || !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if live, err := opts.inspect(); err != nil || len(live) != 1 {
					t.Fatal("returned before tool exit", live, err)
				}
				if _, err := s.Run(context.Background(), "after"); err != nil {
					t.Fatal("settled session not reusable", err)
				}
			} else {
				if !errors.Is(err, ErrUncertain) {
					t.Fatal("surviving tool did not disable session", err)
				}
				if _, err := s.Run(context.Background(), "after"); !errors.Is(err, ErrUnavailable) {
					t.Fatal("surviving tool allowed reuse", err)
				}
				if err := s.Close(context.Background()); !errors.Is(err, ErrRecoveryUnsupported) || errors.Is(err, ErrCleanupPending) {
					t.Fatal("live tool failure disappeared during cleanup", err)
				}
				if live, err := opts.inspect(); err != nil || len(live) != 0 {
					t.Fatal("cleanup left processes", live, err)
				}
			}
		})
	}
}
