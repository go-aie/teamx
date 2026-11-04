package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// The demo is a CLI client and process owner. All collaboration goes through
// the same compiled serve/member/send/read entry points used by an operator.
type cliDriver struct {
	ctx      context.Context
	binary   string
	dir      string
	log      io.Writer
	process  *exec.Cmd
	exited   chan error
	logFile  *os.File
	endpoint string
	info     ServiceInfo
	report   demoReport
}

type demoReport struct {
	Scenario string            `json:"scenario"`
	Platform string            `json:"platform"`
	Go       string            `json:"go"`
	StateDir string            `json:"state_dir"`
	Checks   []string          `json:"checks"`
	Stages   []demoStage       `json:"stages"`
	History  []RoomMessage     `json:"history"`
	Sessions []virtualSnapshot `json:"closed_sessions"`
}

type demoStage struct {
	Name     string            `json:"name"`
	Room     serviceMembers    `json:"room"`
	Sessions []virtualSnapshot `json:"sessions,omitempty"`
}

func demo(args []string, stdout, stderr io.Writer) (runErr error) {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("state-dir", "", "new or empty directory (default: a retained temporary directory)")
	timeout := flags.Duration("timeout", 45*time.Second, "total demo execution deadline")
	asJSON := flags.Bool("json", false, "write the acceptance report as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *timeout <= 0 {
		return ErrRequest
	}
	var err error
	if *dir == "" {
		*dir, err = os.MkdirTemp("", "teamx-demo-*")
	} else {
		*dir, err = filepath.Abs(*dir)
		if err == nil {
			err = os.MkdirAll(*dir, 0700)
		}
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(*dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("demo requires a new or empty state directory: %s", *dir)
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalCtx, *timeout)
	defer cancel()
	if fixture := os.Getenv("TEAMX_FIXTURE_CLI"); fixture != "" {
		binary = fixture
	}
	d := &cliDriver{ctx: ctx, binary: binary, dir: *dir, log: stderr,
		report: demoReport{Scenario: "P1-06", Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Go: runtime.Version(), StateDir: *dir}}
	defer func() { runErr = errors.Join(runErr, d.stop()) }()
	fmt.Fprintf(stderr, "demo state_dir=%s\n", *dir)
	if err := d.scenario(); err != nil {
		return fmt.Errorf("demo failed (records retained in %s): %w", *dir, err)
	}
	data, err := json.MarshalIndent(d.report, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(*dir, "demo-report.json")
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, d.report)
	}
	return writeOutput(stdout, []byte(fmt.Sprintf("P1-06 demo passed: %d checks, %d public messages; B resumed its original Session.\nReport: %s\n", len(d.report.Checks), len(d.report.History), path)))
}

func operatorEnv(endpoint string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !isRoomToolEnv(entry) {
			env = append(env, entry)
		}
	}
	if endpoint != "" {
		env = append(env, "TEAMX_ENDPOINT="+endpoint)
	}
	return env
}

func (d *cliDriver) start(number int) error {
	configPath, err := isolatedStartupConfig(d.dir, nil)
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(d.dir, fmt.Sprintf("serve-%d.log", number)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(d.ctx, d.binary, "serve", "--config", configPath, "--room", "design-review", "--state-dir", d.dir,
		"--listen", "127.0.0.1:0", "--merge-window", "0s", "--max-turns", "3")
	cmd.Env, cmd.Stderr, cmd.WaitDelay = operatorEnv(""), log, time.Second
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 7 * time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = log.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = pipe.Close()
		_ = log.Close()
		return err
	}
	d.process, d.logFile, d.exited = cmd, log, make(chan error, 1)
	var ready struct {
		ServiceInfo
		Endpoint string `json:"endpoint"`
	}
	err = json.NewDecoder(pipe).Decode(&ready)
	// Wait starts after readiness has been read: StdoutPipe must not be
	// closed by Wait while the decoder still owns its bytes.
	go func() { d.exited <- cmd.Wait() }()
	if err != nil {
		return fmt.Errorf("serve readiness (see serve-%d.log): %w", number, err)
	}
	d.info, d.endpoint = ready.ServiceInfo, ready.Endpoint
	return nil
}

func (d *cliDriver) stop() error {
	if d.process == nil {
		return nil
	}
	_ = d.process.Process.Signal(os.Interrupt)
	timer := time.NewTimer(7 * time.Second)
	defer timer.Stop()
	var err error
	select {
	case err = <-d.exited:
	case <-timer.C:
		_ = d.process.Process.Kill()
		err = errors.Join(fmt.Errorf("demo service cleanup timed out"), <-d.exited)
	}
	err = errors.Join(err, d.logFile.Close())
	d.process, d.logFile = nil, nil
	return err
}

func (d *cliDriver) command(out any, args ...string) error {
	cmd := exec.CommandContext(d.ctx, d.binary, append(args, "--json")...)
	cmd.Env, cmd.WaitDelay = operatorEnv(d.endpoint), time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &boundedWriter{w: &stdout, remaining: 4 << 20}
	cmd.Stderr = &boundedWriter{w: &stderr, remaining: 32 << 10}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("CLI %v: %w: %s", args, err, stderr.String())
	}
	if out != nil {
		return json.Unmarshal(stdout.Bytes(), out)
	}
	return nil
}

func (d *cliDriver) members() (serviceMembers, error) {
	var list serviceMembers
	err := d.command(&list, "member", "list")
	return list, err
}

func findMember(list serviceMembers, agent string) (serviceMember, error) {
	for _, member := range list.Members {
		if member.AgentID == agent && member.Execution != nil {
			return member, nil
		}
	}
	return serviceMember{}, fmt.Errorf("member %s unavailable", agent)
}

func (d *cliDriver) wait(agent string, turns uint64) (serviceMembers, error) {
	for {
		list, err := d.members()
		if err != nil {
			return list, err
		}
		member, err := findMember(list, agent)
		if err != nil {
			return list, err
		}
		execution := member.Execution
		if execution.Error != "" || list.WriteError != "" {
			return list, fmt.Errorf("%s execution failed: %s %s", agent, execution.Error, list.WriteError)
		}
		if !execution.Active && execution.Agent.TurnNumber == turns {
			if execution.Agent.Turn == nil || execution.Agent.Turn.Status != "completed" {
				return list, fmt.Errorf("%s turn did not complete", agent)
			}
			return list, nil
		}
		select {
		case <-d.ctx.Done():
			return list, fmt.Errorf("waiting for %s turn %d: %w", agent, turns, d.ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (d *cliDriver) runTurn(agent string, turns uint64, pause bool) error {
	if err := d.command(nil, "member", "run", "--agent", agent); err != nil {
		return err
	}
	if _, err := d.wait(agent, turns); err != nil {
		return err
	}
	if pause {
		return d.command(nil, "member", "pause", "--agent", agent)
	}
	return nil
}

func (d *cliDriver) session(member MemberInfo) (virtualSnapshot, error) {
	var state virtualSnapshot
	err := readSnapshot(filepath.Join(d.dir, "agents", member.MembershipID, "session.json"), &state, maxPrivateFileBytes)
	return state, err
}

func (d *cliDriver) capture(name string) (serviceMembers, error) {
	list, err := d.members()
	if err != nil {
		return list, err
	}
	stage := demoStage{Name: name, Room: list}
	for _, member := range list.Members {
		if member.Execution == nil {
			return list, fmt.Errorf("cannot inspect member %s: %s", member.AgentID, member.Status)
		}
		state, err := d.session(member.MemberInfo)
		if err == nil {
			stage.Sessions = append(stage.Sessions, state)
		} else if !errors.Is(err, os.ErrNotExist) {
			return list, err
		}
	}
	d.report.Stages = append(d.report.Stages, stage)
	fmt.Fprintf(d.log, "%s: messages=%d members=%d\n", name, list.LatestSeq, len(list.Members))
	return list, nil
}

func (d *cliDriver) check(name string, passed bool) error {
	if !passed {
		return fmt.Errorf("acceptance check failed: %s", name)
	}
	d.report.Checks = append(d.report.Checks, name)
	return nil
}

func (d *cliDriver) scenario() error {
	if err := d.start(1); err != nil {
		return err
	}
	firstInfo := d.info
	for _, member := range []struct{ agent, script string }{{"A", "reply,read"}, {"B", "read,read,read,read"}} {
		if err := d.command(nil, "member", "add", "--agent", member.agent, "--profile", "fixture-"+member.script); err != nil {
			return err
		}
	}
	for i, text := range []string{"Review cancellation ordering", "Check persistence before replies", "Keep ordinary output private"} {
		if err := d.command(nil, "send", "--text", text, "--id", fmt.Sprintf("demo-%d", i+1)); err != nil {
			return err
		}
	}
	// Poll the observed notification snapshot, never infer delivery from sleep.
	for {
		list, err := d.members()
		if err != nil {
			return err
		}
		ready := len(list.Members) == 2
		for _, member := range list.Members {
			ready = ready && member.UnreadCount == 3 && member.Notification != nil && member.Notification.UnreadCount == 3
		}
		if ready {
			data, _ := json.Marshal(list)
			if err := d.check("metadata_only_notifications", !bytes.Contains(data, []byte("Keep ordinary output private"))); err != nil {
				return err
			}
			break
		}
		select {
		case <-d.ctx.Done():
			return d.ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := d.capture("three_unread_each"); err != nil {
		return err
	}
	if err := d.runTurn("B", 1, true); err != nil {
		return err
	}
	list, err := d.capture("B_reads_without_posting")
	if err != nil {
		return err
	}
	if err := d.check("B_read_without_post", list.LatestSeq == 3 && list.Members[0].UnreadCount == 3 && list.Members[1].ReadThrough == 3); err != nil {
		return err
	}
	bFirst, err := d.session(list.Members[1].MemberInfo)
	if err != nil {
		return err
	}
	if err := d.runTurn("A", 1, true); err != nil {
		return err
	}
	if err := d.runTurn("B", 2, true); err != nil {
		return err
	}
	list, err = d.capture("A_posts_B_reads_followup")
	if err != nil {
		return err
	}
	aFirst, err := d.session(list.Members[0].MemberInfo)
	if err != nil {
		return err
	}
	bSecond, err := d.session(list.Members[1].MemberInfo)
	if err != nil {
		return err
	}
	if err := d.check("B_followup_reuses_private_session", list.LatestSeq == 4 && bSecond.SessionID == bFirst.SessionID && bSecond.Turns == 2 &&
		len(bSecond.LastRead) == 1 && bSecond.LastRead[0].Author == (Author{Kind: "agent", ID: "A"}) && aFirst.SessionID != bFirst.SessionID); err != nil {
		return err
	}
	if err := d.command(nil, "send", "--text", "Verify reuse on another input", "--id", "demo-followup"); err != nil {
		return err
	}
	if err := d.runTurn("A", 2, true); err != nil {
		return err
	}
	if err := d.runTurn("B", 3, false); err != nil {
		return err
	}
	list, err = d.capture("both_reuse_B_reaches_budget")
	if err != nil {
		return err
	}
	a, b := list.Members[0], list.Members[1]
	aFinal, err := d.session(a.MemberInfo)
	if err != nil {
		return err
	}
	bFinal, err := d.session(b.MemberInfo)
	if err != nil {
		return err
	}
	if err := d.check("both_sessions_reused", aFinal.SessionID == aFirst.SessionID && bFinal.SessionID == bFirst.SessionID &&
		aFinal.Turns == 2 && bFinal.Turns == 3 && a.ReadThrough == 5 && b.ReadThrough == 5); err != nil {
		return err
	}
	retryErr := d.command(nil, "member", "run", "--agent", "B", "--retry")
	if err := d.check("budget_blocks_explicit_retry", b.Status == "budget_exhausted" && b.Execution.Paused &&
		b.Execution.TurnBudget.Used == 3 && retryErr != nil && strings.Contains(retryErr.Error(), "turn_budget")); err != nil {
		return err
	}
	if err := d.command(nil, "member", "remove", "--agent", "A"); err != nil {
		return err
	}
	if err := d.stop(); err != nil {
		return err
	}
	bClosed, err := d.session(b.MemberInfo)
	if err != nil {
		return err
	}
	if err := d.check("shutdown_closes_session", len(bClosed.Events) != 0 && bClosed.Events[len(bClosed.Events)-1] == "session closed"); err != nil {
		return err
	}
	if err := d.start(2); err != nil {
		return err
	}
	list, err = d.capture("same_directory_recovered")
	if err != nil {
		return err
	}
	if err := d.check("restart_preserves_membership_and_cursor", d.info.RoomIdentity == firstInfo.RoomIdentity && d.info.ServiceEpoch != firstInfo.ServiceEpoch &&
		list.LatestSeq == 5 && len(list.Members) == 1 && list.Members[0].MembershipID == b.MembershipID && list.Members[0].ReadThrough == 5 &&
		list.Members[0].Attached && list.Members[0].Execution.Agent.TurnNumber == 3 && list.Members[0].Execution.TurnBudget.Used == 0); err != nil {
		return err
	}
	if err := d.command(nil, "send", "--text", "Verify continuity after restart", "--id", "demo-restart"); err != nil {
		return err
	}
	if _, err := d.wait("B", 4); err != nil {
		return err
	}
	if _, err := d.capture("B_continues_original_session"); err != nil {
		return err
	}
	var page ReadPage // Six messages fit in one CLI page.
	if err := d.command(&page, "read", "--all"); err != nil {
		return err
	}
	d.report.History = page.Messages
	public := len(page.Messages) == 6 && page.SnapshotSeq == 6 && !page.HasMore
	agentPosts := 0
	for _, message := range page.Messages {
		public = public && !strings.Contains(message.Text, "private turn")
		if message.Author.Kind == "agent" {
			agentPosts++
			public = public && message.Author.ID == "A" && message.Seq == 4 && message.ReplyTo == 3 &&
				message.Text == "A read 3 message(s); last: Keep ordinary output private"
		}
	}
	if err := d.check("only_explicit_A_post_is_public", public && agentPosts == 1); err != nil {
		return err
	}
	if err := d.stop(); err != nil {
		return err
	}
	bRestored, err := d.session(b.MemberInfo)
	if err != nil {
		return err
	}
	if err := d.check("original_session_continues_and_closes", bRestored.SessionID == bFirst.SessionID && bRestored.Turns == 4 && bRestored.ScriptPosition == 4 &&
		bRestored.ProcessedThrough == 6 && len(bRestored.LastRead) == 1 && bRestored.LastRead[0].Seq == 6 &&
		strings.Count(strings.Join(bRestored.Events, ";"), "session opened") == 2 && bRestored.Events[len(bRestored.Events)-1] == "session closed"); err != nil {
		return err
	}
	d.report.Sessions = []virtualSnapshot{bRestored}
	return nil
}
