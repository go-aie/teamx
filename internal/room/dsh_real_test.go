//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/testutil"
)

type realRoomDriver struct {
	cliDriver                                                    // Reuse the same real CLI client and sanitized operator identity.
	output, secret, profilePath, workspace, memberDir, sessionID string
	report                                                       map[string]any
	dshProcess                                                   realProcessIdentity
	// Bundle acceptance supplies the archived CLI and the operator's complete
	// model settings, including gateway compatibility. Other drivers still build.
	sessionMaxTurns uint64
	modelOverride   *dsh.ModelConfig
}

type realProcessIdentity struct {
	PID     int    `json:"pid"`
	Parent  int    `json:"parent"`
	Group   int    `json:"group"`
	Started string `json:"started"`
}

// Observe identities only: command lines and process environments can contain
// credentials. These observations are evidence, never targets for signalling.
func realProcesses(ctx context.Context) ([]realProcessIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,pgid=,lstart=")
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	data, err := cmd.Output()
	if err != nil {
		return nil, errors.New("cannot observe real DSH process identities")
	}
	var rows []realProcessIdentity
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 8 {
			return nil, errors.New("invalid process identity observation")
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		group, e3 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, errors.New("invalid observed PID")
		}
		rows = append(rows, realProcessIdentity{pid, parent, group, strings.Join(fields[3:], " ")})
	}
	return rows, nil
}

// Bubblewrap gives the tool its own PID namespace. Resolve its reported $$ to
// an observed descendant of DSH before cancellation, without reading argv/env.
func realToolProcess(ctx context.Context, owner, toolPID int) (realProcessIdentity, error) {
	rows, err := realProcesses(ctx)
	if err != nil {
		return realProcessIdentity{}, err
	}
	parents := make(map[int]int, len(rows))
	for _, row := range rows {
		parents[row.PID] = row.Parent
	}
	var matches []realProcessIdentity
	for _, row := range rows {
		parent := row.Parent
		for n := 0; n < len(rows) && parent > 1 && parent != owner; n++ {
			parent = parents[parent]
		}
		if parent != owner {
			continue
		}
		pid := row.PID
		if runtime.GOOS == "linux" {
			data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", row.PID))
			if err != nil {
				continue
			}
			pid = 0
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == "NSpid:" {
					pid, _ = strconv.Atoi(fields[len(fields)-1])
					break
				}
			}
		}
		if pid == toolPID {
			matches = append(matches, row)
		}
	}
	if len(matches) != 1 {
		return realProcessIdentity{}, errors.New("cannot uniquely observe cancellation tool in DSH process tree")
	}
	return matches[0], nil
}

func TestRealToolProcessRequiresObservedDescendant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exec sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	observed, err := realToolProcess(ctx, os.Getpid(), cmd.Process.Pid)
	if err != nil || observed.PID != cmd.Process.Pid || observed.Started == "" {
		t.Fatalf("owned tool not observed: %+v, %v", observed, err)
	}
	if _, err := realToolProcess(ctx, cmd.Process.Pid, cmd.Process.Pid); err == nil {
		t.Fatal("process outside owner's descendants accepted")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err := realToolProcess(ctx, os.Getpid(), observed.PID); err == nil {
		t.Fatal("exited tool accepted as live evidence")
	}
}

func (d *realRoomDriver) phase(name string) { d.report["phase"] = name }

func (d *realRoomDriver) setup() error {
	d.phase("installed-runtime")
	installation, err := testutil.InstalledDSH(d.ctx)
	if err != nil {
		return err
	}
	d.report["installation"] = installation
	credentialCtx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	keyEnv := os.Getenv("TEAMX_DSH_API_KEY_ENV")
	if d.modelOverride != nil {
		keyEnv = d.modelOverride.APIKeyEnv
	}
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(keyEnv) {
		return errors.New("invalid credential environment name")
	}
	d.secret = os.Getenv(keyEnv)
	if file := os.Getenv("TEAMX_DSH_ENV_FILE"); file != "" && d.secret == "" {
		key, err := exec.CommandContext(credentialCtx, "node", "--env-file="+file, "-e", "process.stdout.write(process.env[process.argv[1]] || '')", keyEnv).Output()
		if err != nil {
			return errors.New("cannot load credential file")
		}
		d.secret = string(key)
	}
	if d.secret == "" || os.Getenv("MODEL_BASE_URL") == "" {
		return errors.New("model credential and MODEL_BASE_URL are required")
	}
	model := os.Getenv("MODEL_ID")
	if model == "" {
		return errors.New("MODEL_ID is required for explicit real acceptance")
	}
	d.report["model"], d.report["baseURL"], d.report["apiKeyEnv"] = model, os.Getenv("MODEL_BASE_URL"), keyEnv
	d.workspace = filepath.Join(d.output, "workspace")
	for _, path := range []string{d.workspace, d.dir} {
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
	}
	private, err := newOpaqueID()
	if err != nil {
		return err
	}
	d.report["privateMarker"] = private
	profile := dshProfile{
		BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs,
		Workspace: d.workspace,
		Instructions: "You are a single TeamX acceptance reviewer. Read new messages with the configured TeamX CLI and follow their requests. " +
			"Keep the private token from the first message in your conversation memory. Include it and this private role marker in ordinary private answers: " + private + ". " +
			"Never send the private role marker to the Room. Only send when a message explicitly requests a public acknowledgement. " +
			"For later memory checks, use conversation memory rather than rereading old Room messages or instructions files. Never delegate or manage members.",
		Model: dsh.ModelConfig{Protocol: "openai-completions", BaseURL: os.Getenv("MODEL_BASE_URL"), ModelID: model, APIKeyEnv: keyEnv},
	}
	if d.modelOverride != nil {
		profile.Model = d.modelOverride.Clone()
	}
	d.profilePath = filepath.Join(d.output, "dsh-profiles.json")
	config, err := json.MarshalIndent(map[string]dshProfile{"reviewer": profile}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(d.profilePath, config, 0600); err != nil {
		return err
	}
	if d.binary != "" {
		return nil
	}
	d.binary = filepath.Join(d.output, "teamx")
	d.phase("build-cli")
	build := exec.CommandContext(d.ctx, "go", "build", "-o", d.binary, "../../cmd/teamx")
	if err := build.Run(); err != nil {
		return fmt.Errorf("build TeamX CLI: %w", err)
	}
	return nil
}

func (d *realRoomDriver) startReal(number int) error {
	return d.startRealRoom(number, "dsh-acceptance")
}

func (d *realRoomDriver) startRealRoom(number int, roomID string) error {
	configPath, err := isolatedStartupConfig(d.output, nil)
	if err != nil {
		return err
	}
	d.phase(fmt.Sprintf("serve-%d-readiness", number))
	log, err := os.OpenFile(filepath.Join(d.output, fmt.Sprintf("serve-%d.log", number)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	threshold := d.sessionMaxTurns
	if threshold == 0 {
		threshold = 8
	}
	cmd := exec.Command(d.binary, "serve", "--config", configPath, "--room", roomID, "--state-dir", d.dir,
		"--listen", "127.0.0.1:0", "--merge-window", "0s", "--session-max-turns", strconv.FormatUint(threshold, 10), "--dsh-config", d.profilePath)
	cmd.Env = append(d.operatorEnv(""), d.report["apiKeyEnv"].(string)+"="+d.secret)
	cmd.Stderr = log
	pipe, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		_ = log.Close()
		return err
	}
	d.process, d.logFile, d.exited = cmd, log, make(chan error, 1)
	d.report["servicePID"] = cmd.Process.Pid
	ready := make(chan error, 1)
	var info struct {
		ServiceInfo
		Endpoint string `json:"endpoint"`
	}
	go func() { ready <- json.NewDecoder(pipe).Decode(&info) }()
	select {
	case err = <-ready:
	case <-time.After(20 * time.Second):
		_ = pipe.Close()
		<-ready
		err = errors.New("Room readiness timed out")
	}
	go func() { d.exited <- cmd.Wait() }()
	if err != nil {
		return fmt.Errorf("Room readiness failed; see serve-%d.log: %w", number, err)
	}
	d.info, d.endpoint = info.ServiceInfo, info.Endpoint
	return nil
}

func (d *realRoomDriver) stopReal() error {
	if d.process == nil {
		return nil
	}
	_ = d.process.Process.Signal(os.Interrupt)
	select {
	case err := <-d.exited:
		closeErr := d.logFile.Close()
		d.process, d.logFile = nil, nil
		return errors.Join(err, closeErr)
	case <-time.After(30 * time.Second):
		// Keep the service alive as cleanup owner. Never kill its parent and
		// then claim that detached DSH/tools have stopped.
		return errors.New("service cleanup unconfirmed; retained service PID in report; inspect before any new execution")
	}
}

func (d *realRoomDriver) waitReal(turn uint64, status string) (serviceMembers, error) {
	var list serviceMembers
	err := d.until(func() (bool, error) {
		var err error
		list, err = d.members()
		if err != nil {
			return false, err
		}
		m, err := findMember(list, "reviewer")
		if err != nil {
			return false, err
		}
		e := m.Execution
		if list.WriteError != "" {
			return false, errors.New(list.WriteError)
		}
		if e.Active || e.Agent.TurnNumber < turn {
			return false, nil
		}
		if e.Agent.TurnNumber != turn || e.Agent.Turn == nil || e.Agent.Turn.Status != status || (status == "completed" && e.Error != "") {
			return false, fmt.Errorf("unexpected settled turn: %+v", e)
		}
		return true, nil
	})
	return list, err
}

func (d *realRoomDriver) until(check func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Minute+20*time.Second)
	defer cancel()
	for {
		ok, err := check()
		if err != nil || ok {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (d *realRoomDriver) privateResult(name, reason string, tokens ...string) error {
	var private struct {
		Result dsh.TurnResult `json:"result"`
		Error  string         `json:"error"`
	}
	if err := readSnapshot(filepath.Join(d.memberDir, "dsh-turn.json"), &private, maxPrivateFileBytes); err != nil {
		return err
	}
	d.report[name] = private
	if private.Result.StopReason != reason || private.Result.TextTruncated || private.Result.ToolsTruncated ||
		(reason == "end_turn" && private.Error != "") || len(private.Result.Tools) == 0 {
		return fmt.Errorf("%s lacks successful CLI tools or expected prompt terminal", name)
	}
	for _, token := range tokens {
		if !strings.Contains(private.Result.Text, token) {
			return fmt.Errorf("%s lacks private memory/role evidence", name)
		}
	}
	var ref struct {
		SessionID string `json:"sessionId"`
	}
	if err := jsonFile(filepath.Join(d.memberDir, "session.json.dsh", "backend.json"), &ref); err != nil {
		return err
	}
	if ref.SessionID == "" || (d.sessionID != "" && d.sessionID != ref.SessionID) {
		return errors.New("DSH SessionID missing or replaced")
	}
	d.sessionID = ref.SessionID
	d.report["sessionId"] = ref.SessionID
	return nil
}

// Component metadata is owned by dsh, whose JSON fields differ from Room's.
func jsonFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (d *realRoomDriver) accept() error {
	if err := d.setup(); err != nil {
		return err
	}
	if err := d.startReal(1); err != nil {
		return err
	}
	d.phase("read-without-posting")
	var member MemberInfo
	if err := d.command(&member, "member", "add", "--agent", "reviewer", "--runner", "dsh", "--profile", "reviewer"); err != nil {
		return err
	}
	d.memberDir = filepath.Join(d.dir, "agents", member.MembershipID)
	memory, err := newOpaqueID()
	if err != nil {
		return err
	}
	private := d.report["privateMarker"].(string)
	if err := d.command(nil, "send", "--text", "Please read this message and remain silent in the Room. Remember this token privately for later: "+memory+". Give a private answer only.", "--id", "read-only"); err != nil {
		return err
	}
	var list serviceMembers
	if err := d.until(func() (bool, error) {
		var err error
		list, err = d.members()
		if err != nil {
			return false, err
		}
		m, err := findMember(list, "reviewer")
		if err != nil {
			return false, err
		}
		return m.Execution.Notification != nil && m.Execution.Notification.UnreadCount == 1 &&
			m.Execution.Notification.NewestUnreadSeq == 1 && !m.Execution.Active && m.Execution.Paused, nil
	}); err != nil {
		return err
	}
	metadata, _ := json.Marshal(list)
	if bytes.Contains(metadata, []byte(memory)) || bytes.Contains(metadata, []byte(private)) {
		return errors.New("Room notification exposed message body or private instructions")
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer"); err != nil {
		return err
	}
	list, err = d.waitReal(1, "completed")
	if err != nil {
		return err
	}
	if list.LatestSeq != 1 || list.Members[0].ReadThrough != 1 || list.Members[0].UnreadCount != 0 {
		return errors.New("read-only turn did not read silently")
	}
	if err := d.privateResult("readOnly", "end_turn", memory, private); err != nil {
		return err
	}
	d.report["readOnlyMember"] = list
	rows, err := realProcesses(d.ctx)
	if err != nil {
		return err
	}
	children := 0
	for _, row := range rows {
		if row.Parent == d.process.Process.Pid && row.Group == row.PID {
			children++
			d.dshProcess = row
		}
	}
	if children != 1 {
		return errors.New("expected one observed DSH child of Room service")
	}
	d.report["dshProcess"] = d.dshProcess

	d.phase("explicit-post-and-memory")
	public, err := newOpaqueID()
	if err != nil {
		return err
	}
	if err := d.command(nil, "send", "--text", "Please read this new message, then explicitly send one public acknowledgement containing "+public+". In your private answer also recall the token from the preceding turn, without rereading old messages.", "--id", "request-post"); err != nil {
		return err
	}
	list, err = d.waitReal(2, "completed")
	if err != nil {
		return err
	}
	if list.LatestSeq != 3 || list.Members[0].ReadThrough < 2 || list.Members[0].UnreadCount != 0 {
		return errors.New("explicit acknowledgement or second read missing")
	}
	if err := d.privateResult("explicitPost", "end_turn", memory, private); err != nil {
		return err
	}
	var page ReadPage
	if err := d.command(&page, "read", "--all"); err != nil {
		return err
	}
	if err := verifyRealPublic(page, 3, public, private); err != nil {
		return err
	}
	d.report["historyAfterPost"] = page

	d.phase("cancel-running-tool")
	if err := d.command(nil, "send", "--text", `Read this message, then use bash to run exactly: printf '%s' "$$" > cancel.pid; printf ready > cancel-started; sleep 90; printf late > cancel-finished. This tests cancellation of a FOREGROUND tool: explicitly set run_in_background=false and timeout=120000. Do not start a background job, append &, or use jobs tools. Wait for the foreground call; do not post.`, "--id", "cancel-tool"); err != nil {
		return err
	}
	if err := d.until(func() (bool, error) {
		_, err := os.Stat(filepath.Join(d.workspace, "cancel-started"))
		if errors.Is(err, os.ErrNotExist) {
			list, listErr := d.members()
			if listErr != nil {
				return false, listErr
			}
			if e := list.Members[0].Execution; !e.Active && e.Agent.TurnNumber >= 3 {
				return false, errors.New("cancel turn ended before tool start")
			}
			return false, nil
		}
		return err == nil, err
	}); err != nil {
		return err
	}
	pidData, err := os.ReadFile(filepath.Join(d.workspace, "cancel.pid"))
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(string(pidData))
	if err != nil || pid <= 1 {
		return errors.New("invalid cancellation tool PID")
	}
	toolProcess, err := realToolProcess(d.ctx, d.dshProcess.PID, pid)
	if err != nil {
		return err
	}
	d.report["cancelToolProcess"] = toolProcess
	started := time.Now()
	if err := d.command(nil, "member", "pause", "--agent", "reviewer", "--cancel"); err != nil {
		return err
	}
	list, err = d.waitReal(3, "interrupted")
	if err != nil {
		return err
	}
	if !list.Members[0].Execution.Paused || list.Members[0].Execution.Agent.TurnNumber != 3 {
		return errors.New("cancelled member did not remain paused")
	}
	if err := d.privateResult("cancel", "cancelled"); err != nil {
		return err
	}
	rows, err = realProcesses(d.ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.PID == toolProcess.PID && row.Started == toolProcess.Started {
			return errors.New("observed cancellation tool survived settled cancellation")
		}
	}
	if _, err := os.Stat(filepath.Join(d.workspace, "cancel-finished")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("cancelled tool reached late side effect")
	}
	d.report["cancelSettledSeconds"], d.report["cancelToolStopped"] = time.Since(started).Seconds(), true
	d.report["cancelMember"] = list
	if list.Members[0].Execution.SessionResetRequired {
		return errors.New("cancelled backend requires Session reset; refusing enable/rotation during original-session acceptance")
	}

	d.phase("same-session-after-cancel")
	if err := d.command(nil, "send", "--text", "Read this new message, remain silent in the Room and privately recall the first token. The long tool was cancelled; do not replay it.", "--id", "after-cancel"); err != nil {
		return err
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer"); err != nil {
		return err
	}
	list, err = d.waitReal(4, "completed")
	if err != nil {
		return err
	}
	if list.LatestSeq != 5 || list.Members[0].ReadThrough != 5 || list.Members[0].UnreadCount != 0 {
		return errors.New("post-cancel read/silence not demonstrated")
	}
	if err := d.privateResult("afterCancel", "end_turn", memory, private); err != nil {
		return err
	}

	d.phase("service-shutdown")
	if err := d.stopReal(); err != nil {
		return err
	}
	// This lock is released only after component process/reader/worker cleanup.
	lock, err := lockDirectory(filepath.Join(d.memberDir, "session.json.dsh"))
	if err != nil {
		return fmt.Errorf("DSH cleanup did not release directory: %w", err)
	}
	if err := lock.Close(); err != nil {
		return err
	}
	rows, err = realProcesses(d.ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.PID == d.dshProcess.PID && row.Started == d.dshProcess.Started {
			return errors.New("observed DSH process survived service shutdown")
		}
	}
	d.report["dshProcessStopped"] = true
	d.report["shutdown"] = "service exited successfully; DSH ownership lock released; records retained"

	d.phase("restart-original-session-ready")
	previous := d.info
	if err := d.startReal(2); err != nil {
		return err
	}
	list, err = d.members()
	if err != nil {
		return err
	}
	m, err := findMember(list, "reviewer")
	if err != nil {
		return err
	}
	if d.info.RoomIdentity != previous.RoomIdentity || d.info.ServiceEpoch == previous.ServiceEpoch ||
		m.MembershipID != member.MembershipID || m.ReadThrough != 5 || list.LatestSeq != 5 ||
		m.Execution.Paused || m.Execution.Active || m.Execution.Error != "" {
		return errors.New("restart did not preserve Room state and admit normal recovery")
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer", "--retry"); err != nil {
		return err // No unread input: this must not open or run a Session.
	}
	if err := d.command(&page, "read", "--all"); err != nil {
		return err
	}
	if err := verifyRealPublic(page, 5, public, private); err != nil {
		return err
	}
	d.report["restartedMembers"], d.report["finalHistory"] = list, page
	// A ready service and an empty retry do not prove lazy Session recovery.
	// Send new input and require the original backend ID and private memory.
	d.phase("resume-original-session-prompt")
	if err := d.command(nil, "send", "--text", "Read this new message using the TeamX CLI. Remain silent in the Room. Recall the FIRST private token and your private role marker from conversation memory. Do not reread earlier messages or inspect files for them.", "--id", "after-restart"); err != nil {
		return err
	}
	if _, err := d.waitReal(5, "completed"); err != nil {
		return err
	}
	if err := d.privateResult("afterRestart", "end_turn", memory, private); err != nil {
		return err
	}
	if err := d.command(&page, "read", "--all"); err != nil {
		return err
	}
	if err := verifyRealPublic(page, 6, public, private); err != nil {
		return err
	}
	d.report["resumedOriginalSession"], d.report["finalHistory"] = true, page
	if err := d.command(nil, "member", "remove", "--agent", "reviewer"); err != nil {
		return err
	}
	if err := d.stopReal(); err != nil {
		return err
	}
	store, err := OpenStore(d.dir, "dsh-acceptance")
	if err != nil {
		return err
	}
	defer store.Close()
	for _, name := range []string{"session.json", "dsh-turn.json", "session.json.dsh/backend.json"} {
		if _, err := os.Stat(filepath.Join(d.memberDir, name)); err != nil {
			return fmt.Errorf("closed member records missing: %w", err)
		}
	}
	d.phase("complete")
	return nil
}

func verifyRealPublic(page ReadPage, expected uint64, acknowledgement, private string) error {
	if page.HasMore || page.SnapshotSeq != expected || uint64(len(page.Messages)) != expected {
		return errors.New("public history is incomplete")
	}
	posts := 0
	for i, message := range page.Messages {
		if message.Seq != uint64(i+1) {
			return errors.New("public history sequence is incomplete")
		}
		if strings.Contains(message.Text, private) {
			return errors.New("private model output leaked into Room")
		}
		if message.Author.Kind == "agent" {
			posts++
			if message.Author.ID != "reviewer" || message.Seq != 3 || !strings.Contains(message.Text, acknowledgement) {
				return errors.New("unexpected public agent post")
			}
		}
	}
	if posts != 1 {
		return errors.New("expected exactly one explicit public acknowledgement")
	}
	return nil
}

// Negative evidence cases protect the acceptance from declaring success merely
// because some tool output or some public message exists. No model is used.
func TestDSHAcceptanceRejectsIncompletePublicEvidence(t *testing.T) {
	good := []RoomMessage{
		{Seq: 1, Author: Author{Kind: "human"}, Text: "read silently"},
		{Seq: 2, Author: Author{Kind: "human"}, Text: "please acknowledge nonce"},
		{Seq: 3, Author: Author{Kind: "agent", ID: "reviewer"}, Text: "ack nonce"},
	}
	for _, name := range []string{"valid", "missing", "stale", "paginated", "wrong-author", "wrong-ack", "private-leak", "extra-post", "sequence-gap"} {
		t.Run(name, func(t *testing.T) {
			page := ReadPage{SnapshotSeq: 3, Messages: append([]RoomMessage(nil), good...)}
			switch name {
			case "missing":
				page.Messages = page.Messages[:2]
			case "stale":
				page.SnapshotSeq = 2
			case "paginated":
				page.HasMore = true
			case "wrong-author":
				page.Messages[2].Author.ID = "other"
			case "wrong-ack":
				page.Messages[2].Text = "unrelated post"
			case "private-leak":
				page.Messages[2].Text += " private-marker"
			case "extra-post":
				page.Messages[0].Author = Author{Kind: "agent", ID: "reviewer"}
			case "sequence-gap":
				page.Messages[1].Seq = 9
			}
			if err := verifyRealPublic(page, 3, "nonce", "private-marker"); (err == nil) != (name == "valid") {
				t.Fatalf("evidence verdict for %s: %v", name, err)
			}
		})
	}
}

func TestDSHAcceptanceRejectsMissingPrivateAndSessionEvidence(t *testing.T) {
	for _, name := range []string{"valid", "wrong-stop", "error", "no-tools", "missing-token", "truncated", "replaced-session", "missing-session"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "session.json.dsh"), 0700); err != nil {
				t.Fatal(err)
			}
			d := realRoomDriver{memberDir: dir, sessionID: "original", report: map[string]any{}}
			result := dsh.TurnResult{StopReason: "end_turn", Text: "memory role", Tools: []dsh.ToolUpdate{{ID: "cli", Status: "completed"}}}
			sessionID, failure := "original", ""
			switch name {
			case "wrong-stop":
				result.StopReason = "max_tokens"
			case "error":
				failure = "failed"
			case "no-tools":
				result.Tools = nil
			case "missing-token":
				result.Text = "role only"
			case "truncated":
				result.TextTruncated = true
			case "replaced-session":
				sessionID = "replacement"
			case "missing-session":
				sessionID = ""
			}
			for path, value := range map[string]any{
				"dsh-turn.json":                 map[string]any{"result": result, "error": failure},
				"session.json.dsh/backend.json": map[string]any{"sessionId": sessionID},
			} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, path), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.privateResult("turn", "end_turn", "memory", "role"); (err == nil) != (name == "valid") {
				t.Fatalf("private evidence verdict for %s: %v", name, err)
			}
		})
	}
}
