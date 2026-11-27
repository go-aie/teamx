package lark

import (
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// ClientFactory shares request queues across clients, including pinned clients.
// A nil or zero factory uses the process-wide production queues. A constructed factory
// owns an isolated set of queues with the supplied pacing; queue ordering,
// cancellation and self-send capacity remain enforced even with zero intervals.
type ClientFactory struct {
	executable string // trusted runtime binding; persisted profiles remain unchanged
	tempRoot   string
	storage    *cliStorage
	requests   *requestBudget
	selfSend   *requestBudget
	gates      *sync.Map
}

func NewClientFactory(interval, selfSendInterval, cooldown time.Duration) *ClientFactory {
	return &ClientFactory{
		gates:    &sync.Map{},
		requests: &requestBudget{interval: interval, cooldown: cooldown},
		selfSend: &requestBudget{interval: selfSendInterval, cooldown: cooldown, capacity: 8},
	}
}

// WithExecutable changes only the runtime program, retaining the credential
// backend, request queues and profile gates. Missing programs fail at invocation.
func (f *ClientFactory) WithExecutable(executable string) (*ClientFactory, error) {
	if !validExecutable(executable) {
		return nil, failure("cli_unavailable")
	}
	copy := f.configuredCopy()
	copy.executable = executable
	if copy.storage != nil {
		copy.storage = &cliStorage{root: copy.storage.root, executable: executable}
	}
	return copy, nil
}

func (f *ClientFactory) configuredCopy() *ClientFactory {
	if f == nil {
		return NewClientFactory(AccountRequestInterval, 0, 0)
	}
	return &ClientFactory{executable: f.executable, tempRoot: f.tempRoot, storage: f.storage, requests: f.requests, selfSend: f.selfSend, gates: f.gates}
}

// Executable projects the effective program for local availability checks.
func (f *ClientFactory) Executable(p platform.CLIProfile) string {
	if f != nil && f.executable != "" {
		return f.executable
	}
	return p.Executable
}

func (f *ClientFactory) ManagedProgram() bool { return f != nil && f.executable != "" }
