package dsh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/testutil"
)

// Explicitly enabled acceptance of the installed DSH through the Go component.
// The default suite never uses a real model or reads a credential file.
func TestRealDSHSession(t *testing.T) {
	if os.Getenv("TEAMX_DSH_REAL") != "1" {
		t.Skip("set TEAMX_DSH_REAL=1 for installed DSH/model acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute evidence directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339), "platform": runtime.GOOS + "/" + runtime.GOARCH, "component": "Go internal/agent/dsh", "permission": "workspace-write + ask; extra permission requests cancelled", "resume": "not exercised by this scenario"}
	defer func() {
		if err := atomicJSON(output, "report.json", report); err != nil {
			t.Errorf("report: %v", err)
		}
		t.Logf("acceptance report: %s", filepath.Join(output, "report.json"))
	}()
	installation, err := testutil.InstalledDSH(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report["installation"] = installation
	credentialCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	keyEnv := os.Getenv("TEAMX_DSH_API_KEY_ENV")
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	if !envName.MatchString(keyEnv) {
		t.Fatal("invalid credential environment name")
	}
	if file := os.Getenv("TEAMX_DSH_ENV_FILE"); file != "" && os.Getenv(keyEnv) == "" {
		// Use Node's dotenv parser, capture only the chosen secret in memory.
		key, err := exec.CommandContext(credentialCtx, "node", "--env-file="+file, "-e", "process.stdout.write(process.env[process.argv[1]] || '')", keyEnv).Output()
		if err != nil {
			t.Fatal("cannot load credential file")
		}
		t.Setenv(keyEnv, string(key))
	}
	baseURL := os.Getenv("MODEL_BASE_URL")
	if baseURL == "" {
		t.Fatal("MODEL_BASE_URL is required for explicit real acceptance")
	}
	model := os.Getenv("MODEL_ID")
	if model == "" {
		t.Fatal("MODEL_ID is required for explicit real acceptance")
	}
	workspace := filepath.Join(output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	role, memory, challenge := randomToken(), randomToken(), randomToken()
	if err := os.WriteFile(filepath.Join(workspace, "challenge.txt"), []byte(challenge+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs,
		Workspace: workspace, StateDir: filepath.Join(output, "state"),
		Instructions: "You are the TeamX Session acceptance probe. Your role token is " + role + ". Never delegate. Use only the requested local file commands.",
		Model:        ModelConfig{Protocol: "openai-completions", BaseURL: baseURL, ModelID: model, APIKeyEnv: keyEnv},
	}
	report["baseURL"], report["model"], report["apiKeyEnv"] = baseURL, model, keyEnv
	if err := acceptRealSession(opts, report, memory, role, challenge); err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	report["status"] = "passed"
}

func randomToken() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}

func acceptRealSession(opts Options, report map[string]any, memory, role, challenge string) (runErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	s, err := Open(ctx, opts)
	if err != nil {
		return err
	}
	report["sessionId"], report["backend"] = s.id, s.reference
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		closeErr := s.Close(closeCtx)
		runErr = errors.Join(runErr, closeErr)
		if closeErr == nil {
			s.process.mu.Lock()
			forced := s.process.forced
			s.process.mu.Unlock()
			code := s.process.cmd.ProcessState.ExitCode()
			report["exit"] = map[string]any{"code": code, "forced": forced, "ownedProcessesStopped": true}
			if code != 0 || forced {
				runErr = errors.Join(runErr, errors.New("real DSH required forced cleanup or exited abnormally"))
			}
		} else {
			report["cleanupError"] = closeErr.Error()
		}
	}()
	first, err := s.Run(ctx, "Remember this private token: "+memory+". Use the shell to run `cat challenge.txt | tee receipt.txt` in your workspace. Reply with the file contents and your role token from global instructions.")
	report["first"] = first
	if err != nil {
		return err
	}
	receipt, err := os.ReadFile(filepath.Join(opts.Workspace, "receipt.txt"))
	if err != nil || string(receipt) != challenge+"\n" || !strings.Contains(first.Text, challenge) || !strings.Contains(first.Text, role) || len(first.Tools) == 0 {
		return errors.New("real file tool or independent role not demonstrated")
	}
	report["fileReceiptMatched"] = true
	second, err := s.Run(ctx, "Without tools, repeat the private token I asked you to remember in the preceding turn and your role token.")
	report["second"] = second
	if err != nil {
		return err
	}
	if !strings.Contains(second.Text, memory) || !strings.Contains(second.Text, role) || len(second.Tools) != 0 {
		return errors.New("same-session private memory not demonstrated")
	}
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()
	type turnOutcome struct {
		result TurnResult
		err    error
	}
	done := make(chan turnOutcome, 1)
	go func() {
		result, err := s.Run(turnCtx, `Use the shell to run exactly: printf "%s" "$$" > cancel.pid; printf ready > cancel-started; sleep 30; printf late > cancel-finished. Wait for it to finish.`)
		done <- turnOutcome{result, err}
	}()
	toolDeadline := time.NewTimer(5 * time.Minute)
	defer toolDeadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(opts.Workspace, "cancel-started")); err == nil {
			break
		}
		select {
		case outcome := <-done:
			return fmt.Errorf("cancel turn ended before tool start: %v", outcome.err)
		case <-toolDeadline.C:
			return errors.New("real cancel tool did not start")
		case <-ticker.C:
		}
	}
	started := time.Now()
	turnCancel()
	outcome := <-done
	report["cancel"] = map[string]any{"result": outcome.result, "settlementSeconds": time.Since(started).Seconds()}
	if !errors.Is(outcome.err, context.Canceled) || outcome.result.StopReason != "cancelled" {
		return errors.New("real prompt did not settle as cancelled")
	}
	pidData, err := os.ReadFile(filepath.Join(opts.Workspace, "cancel.pid"))
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(string(pidData))
	if err != nil {
		return errors.New("invalid real tool PID receipt")
	}
	if err := signalPID(pid, 0); !errors.Is(err, syscall.ESRCH) {
		return errors.New("cancelled tool is still alive or cannot be observed")
	}
	report["cancelToolStopped"] = true
	if _, err := os.Stat(filepath.Join(opts.Workspace, "cancel-finished")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("cancelled tool reached late side effect")
	}
	after, err := s.Run(ctx, "Reply with exactly: after-cancel")
	report["afterCancel"] = after
	if err != nil {
		return err
	}
	if !strings.Contains(after.Text, "after-cancel") {
		return errors.New("same session not reusable after cancellation")
	}
	return nil
}
