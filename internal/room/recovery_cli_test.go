package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type recoveryCLI struct {
	ServiceInfo
	Endpoint  string `json:"endpoint"`
	cmd       *exec.Cmd
	logs      bytes.Buffer
	waited    bool
	binary    string
	ctx       context.Context
	tokenFile string
}

func startRecoveryCLI(t *testing.T, ctx context.Context, binary, dir string, options ...string) *recoveryCLI {
	return startRecoveryProcess(t, ctx, binary, dir, false, options...)
}
func startRecoveryProcess(t *testing.T, ctx context.Context, binary, dir string, authenticated bool, options ...string) *recoveryCLI {
	t.Helper()
	p := &recoveryCLI{binary: binary, ctx: ctx}
	configPath := writeStartupConfig(t, t.TempDir(), "")
	if authenticated {
		testAccessConfig(t, filepath.Dir(configPath))
		p.tokenFile = filepath.Join(filepath.Dir(configPath), "auth", "admin-token")
	}
	p.cmd = exec.CommandContext(ctx, binary, append([]string{"serve", "--config", configPath, "--listen", "127.0.0.1:0", "--state-dir", dir, "--merge-window", "0s"}, options...)...)
	p.cmd.Env = cleanRoomEnv()
	pipe, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stderr = &p.logs
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !p.waited {
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
		}
	})
	if err := json.NewDecoder(pipe).Decode(p); err != nil {
		t.Fatal("recovery readiness", err)
	}
	return p
}

func (p *recoveryCLI) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err := p.cmd.Wait()
	p.waited = true
	if err != nil {
		t.Fatalf("service exit: %v: %s", err, p.logs.String())
	}
}

func (p *recoveryCLI) run(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(p.ctx, p.binary, args...)
	cmd.Env = append(cleanRoomEnv(), "TEAMX_ENDPOINT="+p.Endpoint)
	if p.tokenFile != "" {
		cmd.Env = append(cmd.Env, "TEAMX_ADMIN_TOKEN_FILE="+p.tokenFile)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %v: %s", args, err, stderr.String())
	}
	return stdout.Bytes()
}

func (p *recoveryCLI) list(t *testing.T) serviceMembers {
	t.Helper()
	var result serviceMembers
	if err := json.Unmarshal(p.run(t, "member", "list", "--json"), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func (p *recoveryCLI) wait(t *testing.T, turns uint64) serviceMembers {
	t.Helper()
	for {
		list := p.list(t)
		execution := list.Members[0].Execution
		if execution != nil && !execution.Active && execution.Agent.TurnNumber == turns {
			if execution.Agent.Turn.Status != "completed" || execution.Error != "" {
				t.Fatal(execution)
			}
			return list
		}
		select {
		case <-p.ctx.Done():
			t.Fatal("restored turn did not settle", list, p.ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}
