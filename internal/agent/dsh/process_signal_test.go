//go:build darwin || linux

package dsh

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Send the terminal signal to an isolated foreground process group while its
// observer is running. The service receives Ctrl-C; its observer must survive.
func TestProcessObserverSurvivesGroupInterrupt(t *testing.T) {
	if mode := os.Getenv("TEAMX_OBSERVER_SIGNAL_TEST"); mode != "" {
		if mode == "observer" {
			if err := os.WriteFile(os.Getenv("TEAMX_OBSERVER_READY"), nil, 0600); err != nil {
				os.Exit(2)
			}
			time.Sleep(200 * time.Millisecond)
			os.Exit(0)
		}
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		defer signal.Stop(interrupt)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := processTableCommand(ctx)
		cmd.Path, cmd.Args = os.Args[0], []string{os.Args[0], "-test.run=^TestProcessObserverSurvivesGroupInterrupt$"}
		cmd.Env = append(os.Environ(), "TEAMX_OBSERVER_SIGNAL_TEST=observer")
		if err := cmd.Run(); err != nil {
			t.Fatal("Ctrl-C interrupted the ownership observer:", err)
		}
		select {
		case <-interrupt:
		case <-ctx.Done():
			t.Fatal("service did not receive Ctrl-C")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready := filepath.Join(t.TempDir(), "observer-ready")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessObserverSurvivesGroupInterrupt$")
	ownProcess(cmd)
	cmd.Env = append(os.Environ(), "TEAMX_OBSERVER_SIGNAL_TEST=service", "TEAMX_OBSERVER_READY="+ready, "GORACE=atexit_sleep_ms=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("observer did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
