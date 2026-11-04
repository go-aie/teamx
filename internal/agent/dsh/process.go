package dsh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type processIdentity struct {
	pid, parent, group int
	started            string
}

// DSH's fixed local subprocess backend starts tools in separate process
// groups. Track live descendants too: killing only the ACP process group
// would leave those tools running on cancellation failure.
type process struct {
	cmd                             *exec.Cmd
	in, out, stderr                 *os.File
	waitDone, stderrDone, watchDone chan struct{}
	watchCancel                     context.CancelFunc
	mu                              sync.Mutex
	snapshotMu                      sync.Mutex
	inspect                         func() (map[int]processIdentity, error)
	owned                           map[int]processIdentity
	groupSeen, groupGone            bool
	stderrText                      bytes.Buffer
	stderrTruncated                 bool
	forced                          bool
}

func startProcess(opts Options, observationFailed func(error)) (*process, error) {
	childIn, in, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	out, childOut, err := os.Pipe()
	if err != nil {
		_ = childIn.Close()
		_ = in.Close()
		return nil, err
	}
	stderr, childErr, err := os.Pipe()
	if err != nil {
		for _, f := range []*os.File{childIn, in, out, childOut} {
			_ = f.Close()
		}
		return nil, err
	}
	args := append(append([]string(nil), opts.BinaryArgs...), "--profile", "acp")
	cmd := exec.Command(opts.BinaryPath, args...)
	cmd.Dir, cmd.Env = opts.Workspace, environment(opts)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = childIn, childOut, childErr
	ownProcess(cmd)
	err = cmd.Start()
	for _, f := range []*os.File{childIn, childOut, childErr} {
		_ = f.Close()
	}
	if err != nil {
		for _, f := range []*os.File{in, out, stderr} {
			_ = f.Close()
		}
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &process{cmd: cmd, in: in, out: out, stderr: stderr, waitDone: make(chan struct{}), stderrDone: make(chan struct{}), watchDone: make(chan struct{}), watchCancel: cancel, owned: map[int]processIdentity{}, inspect: opts.inspect}
	// Observe before Wait can reap the root and make its PID reusable.
	_, initialObservationErr := p.snapshot()
	go func() { _ = cmd.Wait(); close(p.waitDone) }() // The only Wait for this child.
	go func() { defer close(p.stderrDone); defer stderr.Close(); _, _ = io.Copy(stderrSink{p}, stderr) }()
	go func() {
		defer close(p.watchDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := p.snapshot(); err != nil {
				observationFailed(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	if initialObservationErr != nil {
		observationFailed(initialObservationErr)
	}
	return p, nil
}

type stderrSink struct{ p *process }

func (w stderrSink) Write(data []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	n := min(len(data), (64<<10)-w.p.stderrText.Len())
	_, _ = w.p.stderrText.Write(data[:n])
	w.p.stderrTruncated = w.p.stderrTruncated || n < len(data)
	return len(data), nil
}

// ps contains only identity fields, never environment or command lines. A
// fresh start-time match is required before signalling a tracked detached PID.
func processTable() (map[int]processIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,pgid=,lstart=")
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	cmd.WaitDelay = time.Second
	var output bytes.Buffer
	cmd.Stdout = &cappedBuffer{buffer: &output, limit: 8 << 20}
	if err := cmd.Run(); err != nil {
		return nil, errors.New("cannot inspect owned DSH processes")
	}
	return parseProcessTable(output.String())
}

func parseProcessTable(output string) (map[int]processIdentity, error) {
	table := map[int]processIdentity{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 8 {
			return nil, errors.New("invalid process identity snapshot")
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		group, e3 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, errors.New("invalid process identity")
		}
		table[pid] = processIdentity{pid, parent, group, strings.Join(fields[3:], " ")}
	}
	return table, nil
}

type cappedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *cappedBuffer) Write(data []byte) (int, error) {
	if len(data) > w.limit-w.buffer.Len() {
		return 0, errors.New("diagnostic exceeds limit")
	}
	return w.buffer.Write(data)
}

func (p *process) snapshot() (map[int]processIdentity, error) {
	p.snapshotMu.Lock()
	defer p.snapshotMu.Unlock()
	table, err := p.inspect()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	root := p.cmd.Process.Pid
	// Our new process group also catches children after the root exits. Live
	// detached descendants retain their original identity across reparenting.
	anchored := false
	if !p.groupSeen {
		select {
		case <-p.waitDone:
		default:
			anchored = true
		}
	} else {
		for pid, owner := range p.owned {
			if row, ok := table[pid]; ok && owner.group == root && row.started == owner.started {
				anchored = true
				break
			}
		}
		if !anchored && !p.groupGone {
			for _, row := range table {
				if row.group == root {
					return nil, errors.New("cannot confirm DSH process group identity")
				}
			}
			p.groupGone = true
		}
	}
	if anchored && !p.groupGone {
		for _, row := range table {
			if row.group == root {
				p.owned[row.pid] = row
				p.groupSeen = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, row := range table {
			if _, ok := p.owned[row.pid]; ok {
				continue
			}
			owner, ok := p.owned[row.parent]
			parent, live := table[row.parent]
			if ok && live && owner.started == parent.started {
				p.owned[row.pid] = row
				changed = true
			}
		}
	}
	live := map[int]processIdentity{}
	for pid, owner := range p.owned {
		if row, ok := table[pid]; ok && row.started == owner.started {
			live[pid] = row
		}
	}
	if len(live) > 4096 {
		return nil, errors.New("owned DSH process limit exceeded")
	}
	p.owned = live
	return maps.Clone(live), nil
}

// A cancelled ACP prompt may settle before an asynchronously terminated shell
// has actually exited. Retain the Run slot until only the DSH root remains.
// A surviving tool or lost observation disables the Session; stop owns cleanup.
func (p *process) waitToolsStopped(ctx context.Context) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		live, err := p.snapshot()
		if err != nil {
			return err
		}
		if _, ok := live[p.cmd.Process.Pid]; !ok {
			return errors.New("DSH process exited during cancellation settlement")
		}
		if len(live) == 1 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("cancelled DSH tools did not stop within settlement budget")
		case <-ticker.C:
		}
	}
}

// stop keeps responsibility until both the unique Wait and observed process
// tree disappearance are confirmed. Callers bound their wait, not ownership.
func (p *process) stop(grace time.Duration) {
	_ = p.in.Close()
	// Give stdin EOF/session-close disposal its full budget, including tools
	// that are still draining after the root exits. A caller timing out does
	// not shorten this grace period or release process ownership.
	deadline := time.Now().Add(grace)
	for {
		live, err := p.snapshot()
		if err != nil {
			// The exec.Cmd still owns the direct child handle. Kill that
			// child if possible, but retain the lock without tree proof.
			_ = p.cmd.Process.Kill()
			p.mu.Lock()
			p.forced = true
			p.mu.Unlock()
		}
		if err == nil {
			if len(live) == 0 {
				select {
				case <-p.waitDone:
					p.watchCancel()
					<-p.watchDone
					_ = p.out.Close()
					_ = p.stderr.Close()
					<-p.stderrDone
					return
				default:
				}
			}
			if remaining := time.Until(deadline); remaining > 0 {
				time.Sleep(min(20*time.Millisecond, remaining))
				continue
			}
			// Freeze parents before escalation so tracked tools cannot keep
			// spawning descendants during the next inspection.
			if len(live) > 0 {
				p.mu.Lock()
				p.forced = true
				p.mu.Unlock()
			}
			for pid := range live {
				_ = signalPID(pid, syscall.SIGSTOP)
			}
			if frozen, snapshotErr := p.snapshot(); snapshotErr == nil {
				for pid := range frozen {
					if pid != p.cmd.Process.Pid {
						_ = signalPID(pid, syscall.SIGKILL)
					}
				}
				// Let the parent reap killed children before it too is stopped.
				if _, ok := frozen[p.cmd.Process.Pid]; ok {
					_ = signalPID(p.cmd.Process.Pid, syscall.SIGCONT)
				}
				time.Sleep(50 * time.Millisecond)
				// Recheck identity after the grace period; never signal a bare
				// remembered PID after an intervening process exit.
				if latest, e := p.snapshot(); e == nil {
					for pid := range latest {
						_ = signalPID(pid, syscall.SIGKILL)
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}
