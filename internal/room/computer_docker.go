package room

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	computerdocker "github.com/go-aie/teamx/internal/computer/docker"
)

type computerDocker interface {
	Check(context.Context) (computerdocker.Environment, error)
	Step(context.Context, *computerdocker.State, string) (bool, error)
	Stop(context.Context, *computerdocker.State) error
}

var errComputerDockerWorking = errors.New("Computer Docker operation in progress")
var errComputerManagedManualAlias = errors.New("该实例由本安装的 Docker 托管，请继续使用托管方式；手动连接应填写另一个 Runtime。")

type computerDockerResult struct {
	state computerdocker.State
	done  bool
	err   error
}
type computerDockerWork struct {
	action string
	cancel context.CancelFunc
	result chan computerDockerResult
}

func computerDockerMessage(err error) string {
	// The local adapter emits bounded, credential-free diagnostics. Provider
	// errors may contain upstream bodies, so never publish those diagnostics.
	var e *computer.Error
	if errors.As(err, &e) {
		return "电脑 Runtime 尚未通过连接、身份或执行结清核验，请检查原实例后重试。"
	}
	if errors.Is(err, context.Canceled) {
		return "准备已取消，正在核对本次资源。"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "电脑准备或停止超时，将核对原资源后重试。"
	}
	var local *computerDockerLocalError
	if errors.As(err, &local) {
		return local.Error()
	}
	return "电脑应用待处理，请检查 Docker 与原电脑实例后重试。"
}

type computerDockerLocalError struct{ error }

// dockerAction never waits on a subprocess in the controller loop. A single
// task owns a detached copy; ONLY this loop publishes it into the durable log.
func (c *computerController) dockerAction(action string) (bool, error) {
	c.mu.Lock()
	job := c.dockerWork
	if job == nil {
		if action == "cancel" {
			c.mu.Unlock()
			return true, nil
		}
		if c.journal.Docker == nil {
			c.mu.Unlock()
			return false, ErrInvalid
		}
		state := *c.journal.Docker
		key := ""
		if action == "prepare" {
			var err error
			key, err = c.credentials.get(state.KeyRef)
			if err != nil {
				c.mu.Unlock()
				return false, err
			}
		}
		duration := 15 * time.Minute
		if action == "stop" {
			duration = 65 * time.Second
		}
		ctx, cancel := context.WithTimeout(c.ctx, duration)
		job = &computerDockerWork{action: action, cancel: cancel, result: make(chan computerDockerResult, 1)}
		c.dockerWork = job
		c.mu.Unlock()
		go func() {
			defer cancel()
			r := computerDockerResult{state: state}
			if action == "prepare" {
				r.done, r.err = c.docker.Step(ctx, &r.state, key)
			} else {
				r.err = c.docker.Stop(ctx, &r.state)
				r.done = r.err == nil
			}
			if r.err != nil {
				r.err = &computerDockerLocalError{r.err}
			}
			job.result <- r
			c.signal()
		}()
		return false, errComputerDockerWorking
	}
	c.mu.Unlock()
	select {
	case result := <-job.result:
		c.mu.Lock()
		c.dockerWork = nil
		c.journal.Docker = &result.state
		err := c.persistLocked()
		c.mu.Unlock()
		job.cancel()
		if err != nil {
			return false, err
		}
		if action == "cancel" {
			return true, nil
		}
		if job.action != action {
			c.signal()
			return false, errComputerDockerWorking
		}
		if result.err != nil {
			return false, result.err
		}
		if !result.done {
			c.mu.Lock()
			if current := c.current.Load(); current.Enabled && current.RuntimeMode == "docker" {
				c.ready = false
			}
			c.mu.Unlock()
			c.signal()
			return false, errComputerDockerWorking
		}
		return true, nil
	default:
		return false, errComputerDockerWorking
	}
}
func (c *computerController) finishDockerWorker() {
	c.mu.Lock()
	job := c.dockerWork
	c.dockerWork = nil
	c.mu.Unlock()
	if job != nil {
		job.cancel()
		<-job.result
	}
}
func (c *computerController) cancelDockerPreparation() error {
	c.mu.Lock()
	job := c.dockerWork
	if job != nil && job.action == "prepare" {
		job.cancel()
	}
	c.mu.Unlock()
	_, err := c.dockerAction("cancel")
	return err
}
func (c *computerController) prepareDocker(op *computerChange) error {
	c.mu.Lock()
	if c.journal.Docker == nil {
		ref, err := c.credentials.put(computer.NewID() + computer.NewID())
		if err != nil {
			c.mu.Unlock()
			return err
		}
		d := computerdocker.NewState(c.owner.ComputerID, op.ID, ref, computerdocker.Limits{CPUs: op.Target.DockerCPUs, MemoryGiB: op.Target.DockerMemoryGiB})
		if err = d.Validate(); err != nil {
			c.mu.Unlock()
			return err
		}
		c.journal.Docker = &d
		if err = c.persistLocked(); err != nil {
			c.mu.Unlock()
			return err
		}
	}
	c.mu.Unlock()
	if _, err := c.dockerAction("prepare"); err != nil {
		return err
	}
	c.mu.Lock()
	d := *c.journal.Docker
	c.mu.Unlock()
	op.Target.Endpoint = d.Endpoint
	op.Target.CredentialRef = d.KeyRef
	return nil
}
func (c *computerController) acceptDockerInstance(instance string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.journal.Docker
	if d == nil {
		return ErrInvalid
	}
	if d.Instance != "" && d.Instance != instance {
		return computer.Fail(computer.Stale, "Managed Runtime instance changed")
	}
	if d.Instance == instance {
		return nil
	}
	d.Instance = instance
	return c.persistLocked()
}
func (c *computerController) completeComputerChange(op *computerChange) {
	c.mu.Lock()
	needsStop := c.journal.Docker != nil && !c.journal.Docker.Stopped && (!op.Target.Enabled || op.Target.RuntimeMode != "docker")
	c.mu.Unlock()
	if needsStop {
		op.Stage = "runtime_stop"
		return
	}
	op.Stage = "done"
	op.Status = "completed"
	op.Message = "已保存并应用。"
}
func (c *computerController) restoreDockerForSettlement(config *computerConfig) error {
	ctx, cancel := context.WithTimeout(c.ctx, 8*time.Second)
	defer cancel()
	return c.recoverDocker(ctx, config)
}
func (c *computerController) recoverDocker(ctx context.Context, config *computerConfig) error {
	if _, err := c.dockerAction("prepare"); err != nil {
		return err
	}
	c.mu.Lock()
	d := *c.journal.Docker
	c.mu.Unlock()
	next := *config
	next.Endpoint = d.Endpoint
	next.CredentialRef = d.KeyRef
	if _, info, err := c.checkTarget(ctx, next); err != nil {
		return err
	} else {
		c.observeDockerBoot(info.BootID)
	}
	if next != *config {
		c.document.mu.Lock()
		err := c.document.commitLocked(func(source []byte) ([]byte, func(), error) {
			var disk startupConfig
			if err := decodeComputerTOML(source, &disk); err != nil {
				return nil, nil, err
			}
			actual := defaultComputerConfig(disk.Computer)
			if actual != *config && actual != next {
				return nil, nil, ErrConflict
			}
			raw, err := patchComputerConfig(source, next)
			return raw, func() { c.current.Store(&next) }, err
		})
		c.document.mu.Unlock()
		if err != nil {
			return err
		}
		*config = next
	}
	if c.members != nil {
		return c.members.refreshConnection(next)
	}
	return nil
}

// A Docker-assigned port may change after restart. Its derivation is already
// durable before config publication; accept only this endpoint-only rebind,
// including a crash between config publication and the operation receipt.
func (c *computerController) managedEndpointOnly(actual, before computerConfig) bool {
	if actual.RuntimeMode != "docker" || before.RuntimeMode != "docker" {
		return false
	}
	want := before
	want.Endpoint = actual.Endpoint
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.journal.Docker
	return actual == want && d != nil && d.Endpoint == actual.Endpoint && d.KeyRef == actual.CredentialRef && d.Instance == actual.InstanceID
}

func computerMode(mode string) string {
	if mode == "" {
		return "manual"
	}
	return mode
}

// Runtime boot fencing is independent of the TeamX HTTP service epoch. A
// restarted managed Runtime retires its old lease, so derive a new bounded
// Computer service generation from the authenticated boot, never reuse it.
func (c *computerController) runtimeService(config computerConfig, info computer.Info) string {
	if config.RuntimeMode != "docker" {
		return c.service.ServiceEpoch
	}
	return computer.Hash(struct{ Service, Boot string }{c.service.ServiceEpoch, info.BootID})[:32]
}
func (c *computerController) observeDockerBoot(boot string) {
	c.mu.Lock()
	old := c.dockerBoot
	c.dockerBoot = boot
	if old != "" && old != boot {
		c.ready = false
	}
	c.mu.Unlock()
	if old != "" && old != boot && c.members != nil {
		x := c.members
		x.mu.Lock()
		var entries []*computerMemberExecution
		for _, e := range x.members {
			entries = append(entries, e)
		}
		x.mu.Unlock()
		for _, e := range entries {
			e.mu.Lock()
			if e.resource.Config.RuntimeMode == "docker" {
				e.screen = nil
				e.blocked = true
			}
			e.mu.Unlock()
		}
	}
}
func computerPreparing(op *computerChange) bool {
	return op != nil && op.Target.RuntimeMode == "docker" && op.Target.Enabled && (strings.HasPrefix(op.Stage, "runtime_prepare") || op.Stage == "runtime_ready" || op.Stage == "validate")
}

func (c *computerController) manualComputerAllowed(instance string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.journal.Docker; d != nil && d.Instance != "" && d.Instance == instance {
		return errComputerManagedManualAlias
	}
	return nil
}
