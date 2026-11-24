package room

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	computerdocker "github.com/go-aie/teamx/internal/computer/docker"
	"github.com/go-aie/teamx/internal/persistence"
)

func newComputerID() string { return computer.NewID() }

type computerChangeInput struct {
	ServiceInfo
	InstallationID   string `json:"installation_id"`
	Caller           Caller `json:"caller"`
	ID               string `json:"request_id"`
	ExpectedVersion  uint64 `json:"expected_version"`
	Enabled          bool   `json:"enabled"`
	RuntimeMode      string `json:"runtime_mode"`
	DockerCPUs       int    `json:"docker_cpus"`
	DockerMemoryGiB  int    `json:"docker_memory_gib"`
	Provider         string `json:"provider"`
	Endpoint         string `json:"endpoint"`
	MaxScreens       int    `json:"max_screens"`
	CredentialAction string `json:"credential_action"`
	Credential       string `json:"credential"`
}

// Operation records contain immutable credential references, never key bytes.
type computerChange struct {
	ID             string                 `json:"id"`
	Digest         string                 `json:"digest"`
	Before         computerConfig         `json:"before"`
	Target         computerConfig         `json:"target"`
	Stage          string                 `json:"stage"`
	Status         string                 `json:"status"`
	Message        string                 `json:"message,omitempty"`
	Disruptive     bool                   `json:"disruptive"`
	Members        []memberComputerChange `json:"members,omitempty"`
	Screens        []computer.ScreenInfo  `json:"screens,omitempty"`
	Stopped        int                    `json:"stopped"`
	DisableID      string                 `json:"disable_request_id,omitempty"`
	DisableDigest  string                 `json:"disable_digest,omitempty"`
	DisableApplied bool                   `json:"disable_applied,omitempty"`
	RuntimeReadyBy int64                  `json:"runtime_ready_by,omitempty"`
}
type computerJournal struct {
	Version    int                   `json:"version"`
	Operations []computerChange      `json:"operations"`
	Docker     *computerdocker.State `json:"docker,omitempty"`
}
type computerController struct {
	claimInstance   func(string) error
	closeMu         sync.Mutex
	closed          bool
	members         *computerMembers
	memberEnsure    sync.Mutex
	changeMu        sync.Mutex
	mu              sync.Mutex
	admission       sync.RWMutex // excludes controlled admission while changing its connection
	ensureMu        sync.Mutex
	current         atomic.Pointer[computerConfig]
	document        *configDocument
	credentials     computerCredentials
	owner           computer.Owner
	service         ServiceInfo
	path            string
	journal         computerJournal
	writer          *persistence.Writer
	lock            *os.File
	connect         computer.FactoryForName
	bound           func(computer.Member) (bool, error)
	ctx             context.Context
	cancel          context.CancelFunc
	wake            chan struct{}
	done            chan struct{}
	ready           bool
	running         atomic.Int64
	active          computer.ManagedProvider
	fault           error
	docker          computerDocker
	dockerWork      *computerDockerWork
	advanceMu       sync.Mutex
	recoveryMessage string
	dockerBoot      string
}

func newComputerController(parent context.Context, startup startupConfig, settings *systemSettings, info ServiceInfo, installation, owner string, bound func(computer.Member) (bool, error)) (*computerController, error) {
	if installation == "" || owner == "" || settings.path == "" {
		return nil, ErrIdentity
	}
	home := filepath.Dir(settings.path)
	dir := filepath.Join(home, "computer-state")
	if err := makeDurableDir(dir); err != nil {
		return nil, err
	}
	lock, err := lockDirectoryFile(dir, ".connection.lock")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	c := &computerController{document: settings.configDocument, credentials: computerCredentials{home}, owner: computer.Owner{InstallationID: installation, OwnerID: owner, ComputerID: computer.Hash(struct{ Installation, Owner string }{installation, owner})}, service: info, path: filepath.Join(dir, "connection.json"), journal: computerJournal{Version: 1}, writer: persistence.NewWriter(persistence.DefaultOperations()), lock: lock, connect: computer.Connect, bound: bound, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
	if startup.spaceID != "" {
		c.owner.ComputerID = computer.Hash(struct{ Installation, Owner, Space string }{installation, owner, startup.spaceID})
	}
	c.docker = computerdocker.New()
	initial := defaultComputerConfig(startup.Computer)
	c.current.Store(&initial)
	if err = readSnapshot(c.path, &c.journal, 4<<20); err != nil && !errors.Is(err, os.ErrNotExist) {
		cancel()
		_ = unlockDirectory(lock)
		return nil, err
	}
	pending := 0
	for i := range c.journal.Operations {
		op := &c.journal.Operations[i]
		op.Before = defaultComputerConfig(op.Before)
		op.Target = defaultComputerConfig(op.Target)
		if !validRestartID(op.ID) || op.Target.validate() != nil || op.Before.validate() != nil || op.Stopped < 0 || op.Stopped > len(op.Screens) {
			err = ErrInvalid
			break
		}
		if op.Status != "completed" && op.Status != "failed" {
			pending++
		}
	}
	if c.journal.Docker != nil && (c.journal.Docker.Validate() != nil || c.journal.Docker.Owner != c.owner.ComputerID || !validComputerCredentialRef(c.journal.Docker.KeyRef)) {
		err = ErrInvalid
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) || c.journal.Version != 1 || len(c.journal.Operations) > 32 || pending > 1 {
		cancel()
		_ = unlockDirectory(lock)
		return nil, ErrInvalid
	}
	return c, nil
}
func (c *computerController) start()       { go c.loop() }
func (c *computerController) close() error { return c.closeContext(context.Background()) }
func (c *computerController) closeContext(ctx context.Context) error {
	for !c.closeMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer c.closeMu.Unlock()
	if c.closed {
		return nil
	}
	c.cancel()
	select {
	case <-c.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if c.members != nil {
		if err := c.members.host.Close(ctx); err != nil {
			return err
		}
	}
	if err := c.writer.Close(ctx); err != nil {
		return err
	}
	if err := unlockDirectory(c.lock); err != nil {
		return err
	}
	c.closed = true
	return nil
}
func (c *computerController) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *computerController) pendingLocked() *computerChange {
	for i := range c.journal.Operations {
		op := &c.journal.Operations[i]
		if op.Status != "completed" && op.Status != "failed" {
			return op
		}
	}
	return nil
}
func (c *computerController) persistLocked() error {
	if c.fault != nil {
		return c.fault
	}
	err := c.writer.Save(c.path, c.journal, 4<<20)
	if err != nil {
		c.fault = err
		c.ready = false
	}
	return err
}
func (c *computerController) save(input computerChangeInput) (computerChange, error) {
	c.changeMu.Lock()
	defer c.changeMu.Unlock()

	semantic := input
	semantic.ServiceInfo = ServiceInfo{}
	digest := computer.Hash(semantic)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return computerChange{}, ErrClosing
	}
	if c.fault != nil {
		return computerChange{}, c.fault
	}
	for _, op := range c.journal.Operations {
		if op.ID == input.ID {
			if op.Digest != digest {
				return computerChange{}, ErrConflict
			}
			c.signal()
			return op, nil
		}
		if op.DisableID == input.ID {
			if op.DisableDigest != digest {
				return computerChange{}, ErrConflict
			}
			c.signal()
			return op, nil
		}
	}
	if !validRestartID(input.ID) {
		return computerChange{}, ErrRequest
	}
	before := *c.current.Load()
	if input.RuntimeMode == "" {
		input.RuntimeMode = "manual"
	}
	if input.DockerCPUs == 0 {
		input.DockerCPUs = before.DockerCPUs
		if input.RuntimeMode == "docker" && c.journal.Docker != nil {
			input.DockerCPUs = c.journal.Docker.Limits.CPUs
		}
	}
	if input.DockerMemoryGiB == 0 {
		input.DockerMemoryGiB = before.DockerMemoryGiB
		if input.RuntimeMode == "docker" && c.journal.Docker != nil {
			input.DockerMemoryGiB = c.journal.Docker.Limits.MemoryGiB
		}
	}
	if pending := c.pendingLocked(); pending != nil && !input.Enabled && pending.DisableID == "" && computerPreparing(pending) {
		if before.Version != input.ExpectedVersion {
			return computerChange{}, ErrConflict
		}
		pending.DisableID = input.ID
		pending.DisableDigest = digest
		c.ready = false
		if c.dockerWork != nil && c.dockerWork.action == "prepare" {
			c.dockerWork.cancel()
		}
		if err := c.persistLocked(); err != nil {
			return computerChange{}, err
		}
		c.signal()
		return *pending, nil
	}
	capacityOnly := input.Enabled == before.Enabled && input.RuntimeMode == before.RuntimeMode && input.Provider == before.Provider && (input.Endpoint == before.Endpoint || input.RuntimeMode == "docker") && input.DockerCPUs == before.DockerCPUs && input.DockerMemoryGiB == before.DockerMemoryGiB && input.CredentialAction == "keep"
	if c.pendingLocked() != nil || input.Enabled && !capacityOnly && c.members != nil && c.members.anyPending() {
		return computerChange{}, ErrBusy
	}
	if before.Version != input.ExpectedVersion {
		return computerChange{}, ErrConflict
	}
	target := computerConfig{Enabled: input.Enabled, RuntimeMode: input.RuntimeMode, DockerCPUs: input.DockerCPUs, DockerMemoryGiB: input.DockerMemoryGiB, Provider: input.Provider, Endpoint: input.Endpoint, MaxScreens: input.MaxScreens, Version: before.Version + 1, CredentialRef: before.CredentialRef, OperationID: input.ID}
	var revisedLimits *computerdocker.Limits
	if input.CredentialAction != "keep" && input.CredentialAction != "replace" || input.CredentialAction == "keep" && input.Credential != "" {
		return computerChange{}, ErrRequest
	}
	if target.RuntimeMode == "docker" {
		limits := computerdocker.Limits{CPUs: target.DockerCPUs, MemoryGiB: target.DockerMemoryGiB}
		if err := limits.Validate(); err != nil {
			return computerChange{}, invalidField("computer", err.Error())
		}
		if input.Endpoint != "" || input.Credential != "" || input.CredentialAction != "keep" {
			return computerChange{}, ErrRequest
		}
		target.Endpoint = ""
		target.CredentialRef = ""
		if d := c.journal.Docker; d != nil {
			if d.Limits.CPUs != target.DockerCPUs || d.Limits.MemoryGiB != target.DockerMemoryGiB {
				if d.Container != "" || d.FilesVolume {
					return computerChange{}, invalidField("computer", "已准备创建的电脑 CPU 和内存暂不支持在线更改。")
				}
				revisedLimits = &limits
			}
			target.Endpoint = d.Endpoint
			target.CredentialRef = d.KeyRef
		}
	} else if input.CredentialAction == "replace" {
		ref, err := c.credentials.put(input.Credential)
		if err != nil {
			return computerChange{}, err
		}
		target.CredentialRef = ref
	} else if before.RuntimeMode == "docker" {
		// A generated local management key must never be sent to a newly
		// entered manual endpoint. Its private managed receipt retains it.
		target.CredentialRef = ""
	}
	if err := target.validate(); err != nil {
		return computerChange{}, err
	}
	if revisedLimits != nil {
		c.journal.Docker.Limits = *revisedLimits
	}
	op := computerChange{ID: input.ID, Digest: digest, Before: before, Target: target, Stage: "validate", Status: "applying"}
	if target.RuntimeMode == "docker" && target.Enabled {
		op.Stage = "runtime_prepare"
	}
	if len(c.journal.Operations) == 32 {
		c.journal.Operations = c.journal.Operations[1:]
	}
	c.journal.Operations = append(c.journal.Operations, op)
	if err := c.persistLocked(); err != nil {
		return computerChange{}, err
	}
	// A persisted disable request closes admission before its HTTP receipt.
	if !target.Enabled {
		c.ready = false
	}
	c.signal()
	return op, nil
}
func (c *computerController) provider(config computerConfig) (computer.ManagedProvider, error) {
	key, err := c.credentials.get(config.CredentialRef)
	if err != nil {
		return nil, err
	}
	return c.connect(config.Provider, config.Endpoint, key)
}
func (c *computerController) inspect(ctx context.Context, config computerConfig) (computer.ManagedProvider, computer.Info, error) {
	p, err := c.provider(config)
	if err != nil {
		return nil, computer.Info{}, err
	}
	info, err := p.Inspect(ctx)
	if err != nil {
		return nil, info, err
	}
	if err = checkComputerInfo(info, config.Provider, c.owner); err != nil {
		return nil, info, err
	}
	if _, err = checkComputerInventory(ctx, p, c.owner); err != nil {
		return nil, info, err
	}
	if c.members != nil {
		path := filepath.Join(filepath.Dir(c.path), "contract-"+info.Contract.Digest+".json")
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			if err = c.writer.Save(path, info.Contract, 4<<20); err != nil {
				return nil, info, err
			}
		}
	}
	return p, info, nil
}
func (c *computerController) checkTarget(ctx context.Context, config computerConfig) (computer.ManagedProvider, computer.Info, error) {
	p, info, err := c.inspect(ctx, config)
	if err == nil && (info.InstanceID != config.InstanceID || info.Contract.Digest != config.ContractDigest) {
		err = computer.Fail(computer.Stale, "Computer identity or accepted contract changed; save a new connection explicitly")
	}
	return p, info, err
}
func (c *computerController) loop() {
	defer close(c.done)
	defer c.finishDockerWorker()
	timer := time.NewTicker(3 * time.Second)
	defer timer.Stop()
	for {
		c.advance()
		if c.members != nil {
			c.members.advance()
		}
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		case <-timer.C:
		}
	}
}
func (c *computerController) advance() {
	c.advanceMu.Lock()
	defer c.advanceMu.Unlock()
	c.mu.Lock()
	if c.fault != nil {
		c.mu.Unlock()
		return
	}
	pending := c.pendingLocked()
	var op computerChange
	if pending != nil {
		op = *pending
		op.Screens = append([]computer.ScreenInfo(nil), pending.Screens...)
		if pending.Members != nil {
			op.Members = append([]memberComputerChange{}, pending.Members...)
		}
	}
	current := *c.current.Load()
	ready := c.ready
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	if pending == nil {
		if current.Enabled && (!ready || current.RuntimeMode == "docker") {
			if current.RuntimeMode == "docker" {
				if err := c.recoverDocker(ctx, &current); err != nil {
					if !errors.Is(err, errComputerDockerWorking) {
						c.mu.Lock()
						c.ready = false
						c.recoveryMessage = computerDockerMessage(err)
						c.mu.Unlock()
					}
					return
				}
			}
			p, info, err := c.checkTarget(ctx, current)
			c.mu.Lock()
			if c.pendingLocked() == nil && err == nil {
				c.active = p
				c.ready = true
				c.running.Store(int64(info.Running))
				c.recoveryMessage = ""
			} else if err != nil {
				c.ready = false
			}
			c.mu.Unlock()
		}
		return
	}
	// Controlled screen admissions and calls share this gate. No Actor, Store or config
	// lock is held during validation, remote settlement, or retries.
	if !c.admission.TryLock() {
		return
	}
	defer c.admission.Unlock()
	err := c.step(ctx, &op)
	if errors.Is(err, errComputerDockerWorking) {
		return
	}
	if err != nil {
		op.Status = "waiting"
		op.Message = "连接应用待处理，请检查电脑服务后重试原操作。"
		if op.Stage == "validate" {
			op.Status = "failed"
			op.Message = "连接核验失败，原配置仍然生效。"
		}
		if op.Target.RuntimeMode == "docker" || strings.HasPrefix(op.Stage, "runtime_") {
			op.Status = "waiting"
			op.Message = computerDockerMessage(err)
		}
		if op.Stage == "validate" && op.Target.RuntimeMode == "docker" && op.Before.Enabled && op.Before.RuntimeMode != "docker" {
			op.Stage = "runtime_abort"
			op.Status = "applying"
		}
		if errors.Is(err, errComputerMemberCoordination) {
			op.Message = "检测到已有成员绑定，需要成员能力与 Session 协调后才能应用。"
		}
		if errors.Is(err, errComputerManagedManualAlias) {
			op.Status = "failed"
			op.Message = errComputerManagedManualAlias.Error()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.journal.Operations {
		if c.journal.Operations[i].ID == op.ID {
			// A disable may have arrived while this step was outside the lock.
			if saved := c.journal.Operations[i]; saved.DisableID != "" && op.DisableID == "" {
				op.DisableID = saved.DisableID
				op.DisableDigest = saved.DisableDigest
				op.Status = "applying"
			}
			c.journal.Operations[i] = op
			break
		}
	}
	if err := c.persistLocked(); err != nil {
		// Keep the operation visible as unresolved even when the configuration
		// itself is durable. Reopen reconciles the original ID and target.
		for i := range c.journal.Operations {
			if c.journal.Operations[i].ID == op.ID {
				c.journal.Operations[i].Status = "waiting"
				c.journal.Operations[i].Message = "本地应用记录尚未确认，请重启服务后核对原操作。"
			}
		}
		return
	}
	if op.Status == "applying" {
		c.signal()
	}
}

var errComputerMemberCoordination = errors.New("Computer member coordination is not connected")

func (c *computerController) step(ctx context.Context, op *computerChange) error {
	if op.DisableID != "" && !op.DisableApplied {
		if err := c.cancelDockerPreparation(); err != nil {
			return err
		}
		wanted := op.Target
		op.Before = *c.current.Load()
		op.Target = op.Before
		if !op.Before.Enabled && wanted.RuntimeMode == "docker" {
			op.Target = wanted
		}
		op.Target.Enabled = false
		op.Target.Version++
		op.Target.OperationID = op.ID
		op.Members = nil
		op.Screens = nil
		op.Stopped = 0
		op.Disruptive = op.Before.Enabled
		op.Stage = "settle"
		op.DisableApplied = true
	}
	switch op.Stage {
	case "runtime_prepare":
		if err := c.prepareDocker(op); err != nil {
			return err
		}
		op.Stage = "runtime_ready"
		op.RuntimeReadyBy = time.Now().Add(90 * time.Second).Unix()
	case "runtime_ready":
		_, _, err := c.inspect(ctx, op.Target)
		if err != nil {
			if time.Now().Unix() >= op.RuntimeReadyBy || computer.Is(err, computer.ContractMismatch) || computer.Is(err, computer.Stale) {
				op.Stage = "validate"
			}
			return err
		}
		op.Stage = "validate"
	case "validate":
		if op.Target.Enabled {
			_, info, err := c.inspect(ctx, op.Target)
			if err != nil {
				return err
			}
			if c.claimInstance != nil {
				if err := c.claimInstance(info.InstanceID); err != nil {
					return err
				}
			}
			op.Target.InstanceID = info.InstanceID
			op.Target.ContractDigest = info.Contract.Digest
			if op.Target.RuntimeMode != "docker" {
				if err := c.manualComputerAllowed(info.InstanceID); err != nil {
					return err
				}
			}
			if op.Target.RuntimeMode == "docker" {
				if err := c.acceptDockerInstance(info.InstanceID); err != nil {
					return err
				}
				c.observeDockerBoot(info.BootID)
			}
		}
		op.Disruptive = op.Before.Enabled && (!op.Target.Enabled || op.Before.RuntimeMode != op.Target.RuntimeMode || op.Before.Provider != op.Target.Provider || op.Before.InstanceID != op.Target.InstanceID || op.Before.ContractDigest != op.Target.ContractDigest)
		if op.Disruptive && op.Target.Enabled && c.members != nil && c.members.anyPending() {
			return ErrBusy
		}
		op.Stage = "settle"
	case "settle":
		if op.Before.RuntimeMode == "docker" && op.Before.Enabled {
			if err := c.restoreDockerForSettlement(&op.Before); err != nil {
				return err
			}
		}
		if c.members != nil && (op.Disruptive || !op.Before.Enabled && op.Target.Enabled) {
			collecting := op.Members == nil
			if err := c.members.prepareGlobal(ctx, op); err != nil {
				return err
			}
			if collecting {
				return nil
			}
		}
		if op.Disruptive {
			c.mu.Lock()
			c.ready = false
			c.mu.Unlock()
			p, info, err := c.inspect(ctx, op.Before)
			if err != nil {
				return err
			}
			// Never settle a new instance reached through the old address.
			if info.InstanceID != op.Before.InstanceID {
				return computer.Fail(computer.Stale, "old Computer instance changed")
			}
			inventory, err := p.Screens(ctx, c.owner)
			if err != nil {
				return err
			}
			for _, s := range inventory {
				if c.bound == nil {
					return errComputerMemberCoordination
				}
				bound, err := c.bound(s.Binding.Member)
				if err != nil {
					return err
				}
				if bound && c.members == nil {
					return errComputerMemberCoordination
				}
			}
			if op.Target.Enabled && op.Before.Provider == op.Target.Provider && op.Before.InstanceID == op.Target.InstanceID {
				op.Stage = "commit"
				break
			}
			if op.Screens == nil {
				op.Screens = inventory
				op.Stage = "stop"
				break
			}
		}
		op.Stage = "commit"
	case "stop":
		p, info, err := c.inspect(ctx, op.Before)
		if err != nil {
			return err
		}
		if info.InstanceID != op.Before.InstanceID {
			return computer.Fail(computer.Stale, "old Computer instance changed")
		}
		if op.Stopped < len(op.Screens) {
			saved := op.Screens[op.Stopped]
			inventory, err := p.Screens(ctx, c.owner)
			if err != nil {
				return err
			}
			var live *computer.ScreenInfo
			for i := range inventory {
				if inventory[i].ID == saved.ID {
					live = &inventory[i]
					break
				}
			}
			if live == nil {
				return computer.Fail(computer.Stale, "owned screen disappeared during settlement")
			}
			if live.Binding.Key() != saved.Binding.Key() {
				return computer.Fail(computer.Stale, "screen ownership changed during settlement")
			}
			if live.Status != "stopped" {
				if live.Binding != saved.Binding || live.Epoch != saved.Epoch {
					return computer.Fail(computer.Stale, "screen ownership changed during settlement")
				}
				screen := p.Restore(*live)
				// Unknown work requires an explicit verified stop. Drain alone
				// cannot resolve a blocked screen; other failures remain pending.
				if err = screen.Drain(ctx, computer.Scope{}); err != nil && !computer.Is(err, computer.Unknown) {
					return err
				}
				if err = screen.Stop(ctx); err != nil {
					return err
				}
			}
			op.Stopped++
		} else {
			op.Stage = "commit"
		}
	case "commit":
		var p computer.ManagedProvider
		if op.Target.Enabled {
			var err error
			var info computer.Info
			p, info, err = c.checkTarget(ctx, op.Target)
			if err == nil {
				c.running.Store(int64(info.Running))
			}
			if err != nil {
				return err
			}
		}
		c.document.mu.Lock()
		err := c.document.commitLocked(func(source []byte) ([]byte, func(), error) {
			// Reconcile a durable config commit whose operation receipt was lost.
			var saved startupConfig
			if len(source) > 0 {
				if err := decodeComputerTOML(source, &saved); err != nil {
					return nil, nil, err
				}
			}
			actual := defaultComputerConfig(saved.Computer)
			if actual == op.Target {
				return nil, nil, nil
			}
			if actual != op.Before && !c.managedEndpointOnly(actual, op.Before) {
				return nil, nil, ErrConflict
			}
			next, err := patchComputerConfig(source, op.Target)
			target := op.Target
			return next, func() { c.current.Store(&target) }, err
		})
		c.document.mu.Unlock()
		if err != nil {
			return err
		}
		target := op.Target
		c.current.Store(&target)
		if c.members != nil && target.Enabled && (op.Before.InstanceID == target.InstanceID || target.RuntimeMode == "docker") {
			if err := c.members.refreshConnection(target); err != nil {
				return err
			}
		}
		c.mu.Lock()
		c.active = p
		c.ready = target.Enabled
		if !target.Enabled {
			c.running.Store(0)
		}
		c.mu.Unlock()
		if len(op.Members) > 0 {
			op.Stage = "members"
			break
		}
		c.completeComputerChange(op)
	case "members":
		if c.members == nil {
			return errComputerMemberCoordination
		}
		done, err := c.members.applyGlobal(ctx, op)
		if err != nil {
			return err
		}
		if done {
			c.completeComputerChange(op)
		}
	case "runtime_stop":
		if _, err := c.dockerAction("stop"); err != nil {
			return err
		}
		op.Stage = "done"
		op.Status = "completed"
		op.Message = "已保存并应用，托管电脑已停止。"
	case "runtime_abort":
		if _, err := c.dockerAction("stop"); err != nil {
			return err
		}
		op.Stage = "done"
		op.Status = "failed"
		op.Message = "候选电脑核验失败，已停止本次候选容器，原连接继续生效。"
	default:
		return ErrInvalid
	}
	if op.Status != "completed" && op.Status != "failed" {
		op.Message = ""
		op.Status = "applying"
	}
	return nil
}

// ensureScreen is the shared controlled entrance for member execution and
// operator acceptance. The owner supplies its current service lease.
func (c *computerController) ensureScreen(ctx context.Context, b computer.Binding) (computer.Screen, error) {
	if !c.admission.TryRLock() {
		return nil, computer.Fail(computer.Unavailable, "Computer connection is applying")
	}
	defer c.admission.RUnlock()
	// Serialize capacity reservation without serializing screen tool calls.
	c.ensureMu.Lock()
	defer c.ensureMu.Unlock()
	c.mu.Lock()
	config := *c.current.Load()
	p := c.active
	allowed := c.ctx.Err() == nil && c.ready && config.Enabled && c.pendingLocked() == nil && b.Owner == c.owner
	c.mu.Unlock()
	if !allowed {
		return nil, computer.Fail(computer.Unavailable, "Computer admission is closed")
	}
	screens, err := p.Screens(ctx, c.owner)
	if err != nil {
		return nil, err
	}
	running := 0
	existing := false
	for _, s := range screens {
		if s.Status != "stopped" {
			running++
			if s.Binding.Key() == b.Key() {
				existing = true
			}
		}
	}
	if !existing && running >= config.MaxScreens {
		return nil, computer.Fail(computer.Capacity, "configured Computer screen capacity reached")
	}
	screen, err := p.EnsureScreen(ctx, b)
	if err != nil {
		return nil, err
	}
	if !existing {
		c.running.Store(int64(running + 1))
	}
	return &connectedComputerScreen{Screen: screen, controller: c, instance: config.InstanceID, contract: config.ContractDigest, version: config.Version}, nil
}

// Only these fields are sent over the human settings API.
func (c *computerController) view() any {
	enabledMembers := 0
	if c.members != nil {
		enabledMembers = c.members.enabledCount()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v := *c.current.Load()
	if d := c.journal.Docker; d != nil {
		v.DockerCPUs = d.Limits.CPUs
		v.DockerMemoryGiB = d.Limits.MemoryGiB
	}
	status := "disabled"
	if v.Enabled {
		status = "recovering"
		if c.ready {
			status = "ready"
		}
	}
	ops := make([]computerOperationView, 0, len(c.journal.Operations))
	for _, op := range c.journal.Operations {
		ops = append(ops, c.computerOperationLocked(op))
		if op.Status != "completed" && op.Status != "failed" {
			status = "applying"
		}
	}
	return struct {
		ServiceInfo
		InstallationID  string                  `json:"installation_id"`
		Enabled         bool                    `json:"enabled"`
		Provider        string                  `json:"provider"`
		Endpoint        string                  `json:"endpoint"`
		MaxScreens      int                     `json:"max_screens"`
		Version         uint64                  `json:"version"`
		HasCredential   bool                    `json:"has_credential"`
		Status          string                  `json:"status"`
		Operations      []computerOperationView `json:"operations"`
		Running         int64                   `json:"running_screens"`
		EnabledMembers  int                     `json:"enabled_members"`
		RuntimeMode     string                  `json:"runtime_mode"`
		SuggestedMode   string                  `json:"suggested_runtime_mode"`
		DockerCPUs      int                     `json:"docker_cpus"`
		DockerMemoryGiB int                     `json:"docker_memory_gib"`
		DockerCreated   bool                    `json:"docker_created"`
		Message         string                  `json:"message"`
		CanDisable      bool                    `json:"can_disable"`
	}{c.service, c.owner.InstallationID, v.Enabled, v.Provider, v.Endpoint, v.MaxScreens, v.Version, v.CredentialRef != "", status, ops, c.running.Load(), enabledMembers, v.RuntimeMode, c.suggestedComputerMode(v), v.DockerCPUs, v.DockerMemoryGiB, c.journal.Docker != nil && (c.journal.Docker.Container != "" || c.journal.Docker.FilesVolume), c.recoveryMessage, c.pendingLocked() != nil && c.pendingLocked().DisableID == "" && computerPreparing(c.pendingLocked())}
}

func (c *computerController) suggestedComputerMode(v computerConfig) string {
	if !v.Enabled && v.Endpoint == "" && v.CredentialRef == "" && v.Version == 1 && len(c.journal.Operations) == 0 {
		return "docker"
	}
	return v.RuntimeMode
}

type computerOperationView struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Stage     string `json:"stage"`
	Message   string `json:"message"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
}

func publicComputerOperation(op computerChange) computerOperationView {
	return computerOperationView{op.ID, op.Status, op.Stage, op.Message, op.Stopped, len(op.Screens)}
}

// Caller holds c.mu. The public phase is progress only, never a second durable
// state machine; recovery continues from the connection journal and receipts.
func (c *computerController) computerOperationLocked(op computerChange) computerOperationView {
	v := publicComputerOperation(op)
	if op.Stage != "runtime_prepare" {
		return v
	}
	d := c.journal.Docker
	switch {
	case d == nil || d.Host == "":
		v.Stage = "runtime_check"
	case !d.ImageReady:
		v.Stage = "runtime_image"
	case !d.StateVolume || !d.FilesVolume:
		v.Stage = "runtime_volumes"
	case d.Container == "":
		v.Stage = "runtime_create"
	case !d.TokenInstalled:
		v.Stage = "runtime_connect"
	default:
		v.Stage = "runtime_start"
	}
	return v
}

// Controlled handles retain their original immutable transport, but every new
// call checks current admission. Reconciliation remains available after closure.
type connectedComputerScreen struct {
	computer.Screen
	controller         *computerController
	instance, contract string
	version            uint64
}

func (s *connectedComputerScreen) Call(ctx context.Context, call computer.Call) (computer.Outcome, error) {
	c := s.controller
	if !c.admission.TryRLock() {
		return computer.Outcome{}, computer.Refuse(computer.Unavailable, "Computer connection is applying")
	}
	defer c.admission.RUnlock()
	c.mu.Lock()
	current := c.current.Load()
	allowed := c.ctx.Err() == nil && c.ready && current.Enabled && c.pendingLocked() == nil && current.InstanceID == s.instance && current.ContractDigest == s.contract && current.Version == s.version && c.fault == nil
	c.mu.Unlock()
	if !allowed {
		return computer.Outcome{}, computer.Refuse(computer.Unavailable, "Computer admission is closed")
	}
	return s.Screen.Call(ctx, call)
}

func (s *connectedComputerScreen) display() (computer.DisplayScreen, error) {
	c := s.controller
	c.mu.Lock()
	current := c.current.Load()
	allowed := c.ctx.Err() == nil && c.ready && current.Enabled && c.pendingLocked() == nil && current.InstanceID == s.instance && current.Version == s.version && c.fault == nil
	c.mu.Unlock()
	if !allowed {
		return nil, computer.Refuse(computer.Unavailable, "Computer admission is closed")
	}
	d, ok := s.Screen.(computer.DisplayScreen)
	if !ok {
		return nil, computer.Fail(computer.Unavailable, "provider display unavailable")
	}
	return d, nil
}
func (s *connectedComputerScreen) Control(ctx context.Context, req computer.ControlRequest) (computer.ControlState, error) {
	// Release uses the original transport even after new admission is closed.
	if req.Action == "release" {
		if d, ok := s.Screen.(computer.DisplayScreen); ok {
			return d.Control(ctx, req)
		}
	}
	d, err := s.display()
	if err != nil {
		return computer.ControlState{}, err
	}
	return d.Control(ctx, req)
}
func (s *connectedComputerScreen) View(ctx context.Context, req computer.ViewRequest) (io.ReadWriteCloser, error) {
	d, err := s.display()
	if err != nil {
		return nil, err
	}
	return d.View(ctx, req)
}
func (s *connectedComputerScreen) ViewAsset(ctx context.Context, name string) ([]byte, error) {
	d, err := s.display()
	if err != nil {
		return nil, err
	}
	return d.ViewAsset(ctx, name)
}
