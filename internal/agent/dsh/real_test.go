package dsh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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
