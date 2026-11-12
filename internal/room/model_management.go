package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Model operations retain only immutable targets and commit facts. Session
// progress is read from rotation.json, never duplicated in this journal.
type modelChangeInput struct {
	Caller          Caller     `json:"caller"`
	RequestID       string     `json:"request_id"`
	ServiceEpoch    string     `json:"service_epoch"`
	ExpectedVersion uint64     `json:"expected_version"`
	Kind            string     `json:"kind"` // save, default, selection
	Entry           modelEntry `json:"entry"`
	APIKey          string     `json:"api_key,omitempty"`
	EntryID         string     `json:"entry_id,omitempty"`
	ReplacementID   string     `json:"replacement_id,omitempty"`
	MemberIdentity
	Selection                modelSelection `json:"selection"`
	ExpectedSelectionVersion uint64         `json:"expected_selection_version,omitempty"`
}
type modelChangeMember struct {
	MemberIdentity
	Before           modelSelection       `json:"before"`
	Selection        modelSelection       `json:"selection"`
	BeforeVersion    uint64               `json:"before_version"`
	SelectionVersion uint64               `json:"selection_version"`
	Target           modelSessionSnapshot `json:"target"`
	Rotate           bool                 `json:"rotate"`
	RotationID       string               `json:"rotation_id"`
	Error            string               `json:"error,omitempty"`
}
type modelChangeOperation struct {
	CreatedAt  time.Time           `json:"created_at,omitzero"`
	ID         string              `json:"id"`
	Owner      string              `json:"owner"`
	Digest     string              `json:"digest"`
	Input      modelChangeInput    `json:"input"`
	Credential string              `json:"credential,omitempty"`
	Status     string              `json:"status"`
	Message    string              `json:"message,omitempty"`
	Before     *modelCatalog       `json:"before,omitempty"`
	Target     *modelCatalog       `json:"target,omitempty"`
	Committed  bool                `json:"committed"`
	Members    []modelChangeMember `json:"members"`
}
type modelController struct {
	runtimeSlots chan struct{}
	inspect      func(context.Context, dsh.Options) (dsh.RuntimeIdentity, error)
	manager      *roomManager
	service      *modelService
	ctx          context.Context
	cancel       context.CancelFunc
	workers      sync.WaitGroup
	running      map[string]bool                  // manager.mu
	operations   map[string]*modelChangeOperation // manager.mu
	dir          string
}

func newModelController(ctx context.Context, m *roomManager, c startupConfig, probe func(context.Context, dsh.Options) (dsh.ProbeResult, error)) (*modelController, error) {
	service, err := newModelService(c, m.settings, filepath.Dir(c.path))
	if err != nil {
		return nil, err
	}
	if probe != nil {
		service.probe = probe
	}
	workerCtx, cancel := context.WithCancel(ctx)
	control := &modelController{runtimeSlots: make(chan struct{}, 8), inspect: dsh.InspectRuntime, manager: m, service: service, ctx: workerCtx, cancel: cancel, operations: map[string]*modelChangeOperation{}, running: map[string]bool{}, dir: filepath.Join(filepath.Dir(c.path), "model-operations")}
	if err := makeDurableDir(control.dir); err != nil {
		cancel()
		return nil, err
	}
	files, err := os.ReadDir(control.dir)
	if err != nil {
		cancel()
		return nil, err
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		var op modelChangeOperation
		if err := readSnapshot(filepath.Join(control.dir, file.Name()), &op, maxRoomsFileBytes); err != nil {
			cancel()
			return nil, err
		}
		if !safeMembershipID(op.ID) || file.Name() != op.ID+".json" || op.Input.APIKey != "" {
			cancel()
			return nil, ErrInvalid
		}
		if op.CreatedAt.IsZero() {
			info, err := file.Info()
			if err != nil {
				cancel()
				return nil, err
			}
			op.CreatedAt = info.ModTime()
		}
		control.operations[op.ID] = &op
		switch op.Status {
		case "validating":
			op.Status = "failed"
			if op.Credential != "" {
				_ = os.Remove(filepath.Join(service.credentials.dir, op.Credential))
			}
			op.Message = "验证因服务重启中断，请重新提交验证。"
			_ = control.persist(&op)
		case "prepared", "applying", "failed":
			if op.Target == nil {
				continue
			}
			if !op.Committed {
				// No execution can have closed before this commit fact. Reconcile only
				// local durable evidence, without repeating probes or business turns.
				if op.Input.Kind == "selection" {
					op.Committed = control.selectionCommitted(&op)
				} else {
					op.Committed = reflect.DeepEqual(service.current, *op.Target)
				}
				if !op.Committed && op.Before != nil && reflect.DeepEqual(service.current, *op.Before) {
					op.Status = "failed"
					op.Message = "配置未提交，请重新提交。"
					op.Target = nil
					_ = control.persist(&op)
					continue
				}
			}
			op.Status = "failed"
			op.Message = "服务已重启，请重试同一操作以继续应用。"
			for _, member := range op.Members {
				completed := control.memberCompleted(member)
				if (member.Rotate || op.Input.Kind == "delete") && !completed {
					m.config.models.setGate(member.MemberIdentity, op.ID)
				}
			}
			_ = control.persist(&op)
		}
	}
	return control, nil
}
func (c *modelController) stop() {
	c.manager.mu.Lock()
	c.manager.closing = true
	c.cancel()
	c.manager.mu.Unlock()
	c.workers.Wait()
}
func (c *modelController) persist(op *modelChangeOperation) error {
	return (&Store{ops: defaultSnapshotOps()}).save(filepath.Join(c.dir, op.ID+".json"), op, maxRoomsFileBytes)
}
func (c *modelController) selectionCommitted(op *modelChangeOperation) bool {
	if len(op.Members) != 1 {
		return false
	}
	member := op.Members[0]
	var saved managedRoomConfig
	if readSnapshot(filepath.Join(c.manager.roomDir(member.RoomID), "room-config.json"), &saved, maxPrivateFileBytes) != nil || saved.RoomIdentity != member.RoomIdentity {
		return false
	}
	for _, m := range saved.Members {
		if m.AgentID == member.AgentID && m.MembershipID == member.MembershipID {
			return m.ModelSelection != nil && *m.ModelSelection == member.Selection && m.ModelSelectionVersion == member.SelectionVersion
		}
	}
	return false
}
func modelInputDigest(input modelChangeInput) string {
	input.ServiceEpoch = ""
	data, _ := json.Marshal(input)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (c *modelController) submit(input modelChangeInput, owner string) (any, error) {
	if !safeMembershipID(input.RequestID) || len(input.RequestID) > 128 || input.Kind != "save" && input.Kind != "default" && input.Kind != "selection" && input.Kind != "delete" {
		return nil, ErrRequest
	}
	fingerprint := modelInputDigest(input)
	m := c.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, ErrClosing
	}
	if previous := c.operations[input.RequestID]; previous != nil {
		if previous.Digest != fingerprint || previous.Owner != owner {
			return nil, ErrConflict
		}
		return c.viewLocked(previous), nil
	}
	if len(c.operations) >= 10000 {
		return nil, ErrCapacity
	}
	// Only validation/publication is serialized. Waiting and failed member
	// applications do not own the config slot; disjoint maintenance can proceed.
	for id, running := range c.running {
		if running && c.operations[id].Status == "validating" {
			return nil, ErrBusy
		}
	}
	input.ServiceEpoch = ""
	credential := ""
	if input.APIKey != "" {
		var err error
		credential, err = c.service.credentials.put(input.APIKey)
		if err != nil {
			return nil, err
		}
	}
	input.APIKey = ""
	op := &modelChangeOperation{CreatedAt: time.Now().UTC(), ID: input.RequestID, Owner: owner, Digest: fingerprint, Input: input, Credential: credential, Status: "validating", Members: []modelChangeMember{}}
	if err := c.persist(op); err != nil {
		if credential != "" {
			_ = os.Remove(filepath.Join(c.service.credentials.dir, credential))
		}
		return nil, err
	}
	c.operations[op.ID] = op
	c.launchLocked(op, false)
	return c.viewLocked(op), nil
}
func (c *modelController) launchLocked(op *modelChangeOperation, retry bool) {
	c.running[op.ID] = true
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		if retry {
			c.apply(op)
		} else {
			c.run(op)
		}
		c.manager.mu.Lock()
		delete(c.running, op.ID)
		c.manager.mu.Unlock()
	}()
}
func (c *modelController) run(op *modelChangeOperation) {
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	// Cancellation is service-owned and allowed only before publication.
	input := op.Input
	current := c.service.snapshot()
	var err error
	if current.Version != input.ExpectedVersion {
		err = ErrConflict
	} else if input.Kind == "selection" {
		err = c.changeSelection(ctx, op)
	} else if input.Kind == "delete" {
		err = c.deleteModel(ctx, op)
	} else {
		entry := input.Entry
		if input.Kind == "default" {
			entry, err = current.resolve(modelSelection{Mode: "explicit", EntryID: input.EntryID})
		}
		key := ""
		if op.Credential != "" {
			var credential *dsh.Credential
			credential, err = c.service.credentials.get(op.Credential)
			if err == nil {
				key = credential.Value
			}
		}
		locked := false
		var publish func()
		if err == nil {
			_, err = c.service.save(ctx, modelSave{Entry: entry, APIKey: key, ExpectedVersion: input.ExpectedVersion, MakeDefault: input.Kind == "default", beforeCommit: func(before, next modelCatalog) error {
				c.manager.mu.Lock()
				locked = true
				if op.Status == "cancelled" {
					return context.Canceled
				}
				var err error
				publish, err = c.manager.config.models.publication(next)
				if err != nil {
					return err
				}
				return c.prepareLocked(ctx, op, before, next)
			}, afterCommit: func(modelCatalog) { publish() }})
		}
		if locked {
			if op.Target != nil && reflect.DeepEqual(c.manager.config.models.catalog(), *op.Target) {
				op.Committed = true
				op.Status = "applying"
			}
			if err == nil {
				err = c.persist(op)
			}
			c.manager.mu.Unlock()
		}
	}
	c.manager.mu.Lock()
	if err != nil {
		// An uncertain replace keeps the durable gates; retry first reconciles disk.
		if !op.Committed && !errors.Is(err, ErrCommitUnknown) {
			c.releaseGatesLocked(op)
			op.Target = nil
		}
		if op.Status != "cancelled" {
			op.Status = "failed"
			op.Message = modelChangeError(err)
		}
		_ = c.persist(op)
	}
	c.manager.mu.Unlock()
	if err == nil {
		c.apply(op)
	}
	if op.Credential != "" {
		_ = os.Remove(filepath.Join(c.service.credentials.dir, op.Credential))
	}
}

var errModelRuntimeIdentity = errors.New("member DSH installation differs from validated installation")
var errModelApplication = errors.New("member model rotation failed")
var errModelTemplateReference = errors.New("model is referenced by a template")

func modelChangeError(err error) string {
	if errors.Is(err, errModelTemplateReference) {
		return "模板仍固定引用此模型，请先修正模板再删除。"
	}
	if errors.Is(err, errModelRuntimeIdentity) {
		return "该成员的 DSH 安装与本次验证不一致；请修复安装后重试。"
	}
	if errors.Is(err, errModelApplication) {
		return "该成员结清或切换失败；请查看活动记录，修复后重试本操作。"
	}
	if errors.Is(err, ErrConflict) {
		return "配置或成员版本已变化，请刷新核对。"
	}
	if errors.Is(err, ErrBusy) {
		return "成员有未完成操作，请先处理原操作。"
	}
	if errors.Is(err, ErrCommitUnknown) {
		return "配置提交结果未确认；成员保持阻断，请重试核对磁盘。"
	}
	var probe *dsh.ProbeError
	if errors.As(err, &probe) {
		return err.Error()
	}
	return "操作未完成，请检查模型、凭据、DSH 和成员结清状态后重试。"
}

func (c *modelController) memberCompleted(member modelChangeMember) bool {
	if !member.Rotate {
		var saved managedRoomConfig
		if readSnapshot(filepath.Join(c.manager.roomDir(member.RoomID), "room-config.json"), &saved, maxPrivateFileBytes) != nil || saved.RoomIdentity != member.RoomIdentity {
			return false
		}
		for _, m := range saved.Members {
			if m.AgentID == member.AgentID && m.MembershipID == member.MembershipID {
				return m.ModelSelection != nil && (*m.ModelSelection == member.Selection && m.ModelSelectionVersion == member.SelectionVersion || m.ModelSelectionVersion > member.SelectionVersion && *m.ModelSelection != member.Before)
			}
		}
		return true // exact membership was removed
	}
	root := filepath.Join(c.manager.roomDir(member.RoomID), "agents", member.MembershipID)
	for _, name := range []string{"rotation.json", "rotation-" + member.RotationID + ".json"} {
		var r rotationRecord
		if readSnapshot(filepath.Join(root, name), &r, maxPrivateFileBytes) == nil && r.validate(member.MemberIdentity) == nil && r.OperationID == member.RotationID && r.Phase == "completed" && r.ModelTarget != nil && r.ModelTarget.ExecutionDigest == member.Target.ExecutionDigest {
			return true
		}
	}
	return false
}
