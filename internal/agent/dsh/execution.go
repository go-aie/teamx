package dsh

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/go-aie/teamx/agent"
)

// InputBindingPath keeps the framework's rotating capability in a read-only
// directory mount. Binding an individual file would pin its old inode across
// the owner's atomic replacement; native Sessions retain their original path.
func InputBindingPath(state string, execution agent.ExecutionFactory) string {
	if _, ok := execution.(agent.EndpointRouter); ok {
		return filepath.Join(state, "control", "input-execution.json")
	}
	return filepath.Join(state, "input-execution.json")
}

func routeEndpoints(opts Options) (Options, error) {
	router, ok := selectedExecution(opts).(agent.EndpointRouter)
	if !ok {
		return opts, nil
	}
	var err error
	if opts.Model.BaseURL != "" {
		opts.Model.BaseURL, err = router.RouteEndpoint(opts.Model.BaseURL)
		if err != nil {
			return opts, err
		}
	}
	opts.MCPServers = cloneMCP(opts.MCPServers)
	for i := range opts.MCPServers {
		opts.MCPServers[i].URL, err = router.RouteEndpoint(opts.MCPServers[i].URL)
		if err != nil {
			return opts, err
		}
	}
	opts.ToolEnv = maps.Clone(opts.ToolEnv)
	if endpoint := opts.ToolEnv["TEAMX_ENDPOINT"]; endpoint != "" {
		opts.ToolEnv["TEAMX_ENDPOINT"], err = router.RouteEndpoint(endpoint)
	}
	return opts, err
}

// NativeExecution uses the original process ownership observer. Constructing
// it performs no IO and has no SRT dependency, even on non-Linux platforms.
func NativeExecution() agent.ExecutionFactory { return nativeExecution{} }

type nativeExecution struct {
	inspect func() (map[int]processIdentity, error)
}

func (nativeExecution) Identify(agent.ExecutionRequest) (agent.ExecutionIdentity, error) {
	// Zero preserves the identity of existing native Sessions.
	return agent.ExecutionIdentity{}, nil
}
func (f nativeExecution) Start(ctx context.Context, r agent.ExecutionRequest, failed func(error)) (agent.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := startCommand(r, f.inspect, failed)
	if p == nil {
		return nil, err
	}
	return p, err
}

func selectedExecution(opts Options) agent.ExecutionFactory {
	if opts.Execution != nil {
		return opts.Execution
	}
	return nativeExecution{inspect: opts.inspect}
}
func executionRequest(opts Options, args []string) agent.ExecutionRequest {
	return agent.ExecutionRequest{BinaryPath: opts.BinaryPath, Args: args, Workspace: opts.Workspace,
		StateDir: opts.StateDir, Environment: environment(opts), Binding: opts.ExecutionBinding, Endpoints: executionEndpoints(opts), EnvironmentKeys: []string{opts.Model.APIKeyEnv}}
}
func executionIdentity(opts Options, policy bool) (*agent.ExecutionIdentity, error) {
	r := executionRequest(opts, opts.BinaryArgs)
	if !policy {
		r.StateDir = ""
	}
	id, err := selectedExecution(opts).Identify(r)
	if err != nil || id == (agent.ExecutionIdentity{}) {
		return nil, err
	}
	if !policy {
		id.PolicySHA256 = ""
	}
	return &id, nil
}
func checkExecutionIdentity(opts Options, expected *agent.ExecutionIdentity) error {
	actual, err := executionIdentity(opts, true)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return errors.Join(ErrRecoveryUnsupported, errors.New("execution implementation or policy changed"))
	}
	return nil
}

func (p *process) Stdin() *os.File  { return p.in }
func (p *process) Stdout() *os.File { return p.out }
func (p *process) Stop(grace time.Duration) {
	p.stopOnce.Do(func() { go func() { p.stop(grace); close(p.stopDone) }() })
	<-p.stopDone
}
func (p *process) WaitToolsStopped(ctx context.Context) error { return p.waitToolsStopped(ctx) }
func sameProcessesPresent(expected, live map[int]processIdentity) bool {
	for pid, previous := range expected {
		if current, ok := live[pid]; !ok || previous.started != current.started {
			return false
		}
	}
	return true
}
func (p *process) Ready() error {
	if !p.supervised {
		return p.Check()
	}
	live, err := p.snapshot()
	if err != nil {
		return err
	}
	if _, ok := live[p.cmd.Process.Pid]; !ok {
		return errors.New("execution supervisor exited before ready")
	}
	p.mu.Lock()
	// The pinned relay forks a child for each HTTP/MCP connection. Those
	// connections are auxiliary resources, not permanent infrastructure or
	// tools. Keep their listeners in the baseline and retain every child in
	// the ordinary ownership table for final cleanup.
	p.relays = map[int]processIdentity{}
	for pid, row := range live {
		exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
		if err == nil && slices.Contains(p.relayExecutables, exe) {
			p.relays[pid] = row
		}
	}
	p.baseline = withoutRelayChildren(live, p.relays)
	for pid := range p.relays {
		if _, ok := p.baseline[pid]; !ok {
			delete(p.relays, pid)
		}
	}
	p.mu.Unlock()
	return nil
}

func withoutRelayChildren(live, relays map[int]processIdentity) map[int]processIdentity {
	children := map[int]bool{}
	for changed := true; changed; {
		changed = false
		for pid, row := range live {
			parent, exists := live[row.parent]
			relay, root := relays[row.parent]
			if !children[pid] && exists && ((root && relay.started == parent.started) || children[row.parent]) {
				children[pid], changed = true, true
			}
		}
	}
	out := maps.Clone(live)
	for pid := range children {
		delete(out, pid)
	}
	return out
}
func (p *process) Check() error {
	live, err := p.snapshot()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !sameProcessesPresent(p.baseline, live) {
		return errors.New("execution infrastructure exited")
	}
	return nil
}
func (p *process) Result() error {
	select {
	case <-p.waitDone:
	default:
		return errors.New("execution exit status unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.forced {
		return errors.New("process required forced cleanup")
	}
	if p.cmd.ProcessState == nil || !p.cmd.ProcessState.Success() {
		return fmt.Errorf("execution %s", p.cmd.ProcessState)
	}
	return nil
}

func executionEndpoints(opts Options) []string {
	model := opts.Model.BaseURL
	if model == "" {
		model = "https://api.deepseek.com"
	}
	endpoints := []string{model}
	for _, server := range opts.MCPServers {
		endpoints = append(endpoints, server.URL)
	}
	if endpoint := opts.ToolEnv["TEAMX_ENDPOINT"]; endpoint != "" {
		endpoints = append(endpoints, endpoint)
	}
	return endpoints
}
func (p *process) Diagnostic() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stderrText.String(), p.stderrTruncated
}
