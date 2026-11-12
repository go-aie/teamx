package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func modelSetupRequired(config startupConfig) bool {
	if config.Models != nil {
		return len(config.Models.Entries) == 0
	}
	if requireFreshModelHome(config) == nil {
		return true
	}
	if config.Runtime == nil || validateTeamRuntime(*config.Runtime) != nil {
		return true
	}
	return strings.TrimSpace(legacyModelCredential(config.Runtime.Model.APIKeyEnv, config.Env)) == ""
}

var errModelSetupSaved = errors.New("model setup saved")

type setupModelInput struct {
	RequestID       string `json:"request_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	ModelParameters
	APIKey string `json:"api_key,omitempty"`
}
type setupOperation struct {
	ID      string `json:"operation_id"`
	Digest  string `json:"-"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Candidate keys are private immutable references, never API response fields.
type savedSetupOperation struct {
	Operation       setupOperation `json:"operation"`
	Digest          string         `json:"digest"`
	Entry           modelEntry     `json:"entry"`
	Credential      string         `json:"candidate_credential,omitempty"`
	ExpectedVersion uint64         `json:"expected_version"`
}

type firstModelSetup struct {
	blocked    error
	mu         sync.Mutex
	service    *modelService
	prefill    ModelParameters
	prefillKey string
	path       string
	operation  *savedSetupOperation
	saved      chan struct{}
	ctx        context.Context
	workers    sync.WaitGroup
}

func newFirstModelSetup(ctx context.Context, config startupConfig, settings *systemSettings, probe func(context.Context, dsh.Options) (dsh.ProbeResult, error)) (*firstModelSetup, error) {
	if err := requireFreshModelHome(config); err != nil {
		prefill := ModelParameters{}
		if config.Runtime != nil {
			prefill = parametersFromLegacyModel(config.Runtime.Model)
		}
		return &firstModelSetup{blocked: err, prefill: prefill, saved: make(chan struct{}, 1)}, nil
	}
	original := config
	var prefill ModelParameters
	var key string
	legacyEnv := ""
	if config.Runtime != nil {
		runtime := *config.Runtime
		prefill = parametersFromLegacyModel(runtime.Model)
		legacyEnv = runtime.Model.APIKeyEnv
		legacyCheck := launcherCheckModel()
		legacyCheck.APIKeyEnv = legacyEnv
		if dsh.CheckConfig(dsh.Options{BinaryPath: "/prefill-check", Model: legacyCheck}) == nil && !isRoomToolEnv(legacyEnv) && legacyEnv != "TEAMX_CLI" {
			key = legacyModelCredential(legacyEnv, config.Env)
		}
		runtime.Model = dsh.ModelConfig{}
		config.Runtime = &runtime
	}
	s, err := newModelService(config, settings, filepath.Dir(config.path))
	if err != nil {
		return nil, err
	}
	if probe != nil {
		s.probe = probe
	}
	s.legacyKeyEnv = legacyEnv
	s.removeLegacyModel = original.Runtime != nil
	if s.launcher.BinaryPath == "" {
		s.launcher.BinaryPath, err = exec.LookPath("dsh")
		if err == nil {
			s.launcher.BinaryPath, _ = filepath.Abs(s.launcher.BinaryPath)
		}
	}
	s.launcherConfigMissing = original.Runtime == nil || original.Runtime.DSH.BinaryPath == ""
	if config.Models != nil && len(config.Models.Entries) > 0 {
		entry, err := config.Models.resolve(modelSelection{Mode: "default"})
		if err != nil {
			return nil, err
		}
		prefill = entry.ModelParameters.clone()
		credential, err := s.credentials.get(entry.CredentialRef)
		if err == nil {
			key = credential.Value
		}
	}
	f := &firstModelSetup{service: s, prefill: prefill, prefillKey: key, path: filepath.Join(filepath.Dir(config.path), "model-setup.json"), saved: make(chan struct{}, 1), ctx: ctx}
	var op savedSetupOperation
	if err := readSnapshot(f.path, &op, maxPrivateFileBytes); err == nil {
		if !safeMembershipID(op.Operation.ID) || op.Digest == "" {
			return nil, ErrInvalid
		}
		op.Operation.Digest = op.Digest
		if op.Operation.Status == "validating" {
			if validCredentialRef(op.Credential) {
				_ = os.Remove(filepath.Join(s.credentials.dir, op.Credential))
			}
			op.Credential = ""
			op.Operation.Status, op.Operation.Code, op.Operation.Message = "failed", "interrupted", "验证被服务重启中断，请重新验证。"
			if err := f.persist(&op); err != nil {
				return nil, err
			}
		}
		f.operation = &op
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return f, nil
}

func (f *firstModelSetup) persist(op *savedSetupOperation) error {
	return (&Store{ops: defaultSnapshotOps()}).save(f.path, op, maxPrivateFileBytes)
}
func (f *firstModelSetup) state() any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var operation *setupOperation
	if f.operation != nil {
		copy := f.operation.Operation
		operation = &copy
	}
	version := uint64(0)
	if f.service != nil {
		version = f.service.snapshot().Version
	}
	return struct {
		Writable             bool            `json:"writable"`
		Message              string          `json:"message,omitempty"`
		SetupRequired        bool            `json:"setup_required"`
		Version              uint64          `json:"version"`
		Model                ModelParameters `json:"model"`
		CredentialConfigured bool            `json:"credential_configured"`
		Operation            *setupOperation `json:"operation,omitempty"`
	}{f.blocked == nil, setupBlockedMessage(f.blocked), true, version, f.prefill, f.prefillKey != "", operation}
}

func (f *firstModelSetup) submit(input setupModelInput) (setupOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocked != nil {
		return setupOperation{}, &requestDiagnostic{cause: ErrRequest, Message: setupBlockedMessage(f.blocked)}
	}
	if !safeMembershipID(input.RequestID) {
		return setupOperation{}, ErrRequest
	}
	if input.Protocol == "" {
		input.Protocol = "openai-completions"
	}
	if input.APIKey == "" {
		input.APIKey = f.prefillKey
	}
	if strings.TrimSpace(input.APIKey) == "" {
		return setupOperation{}, invalidField("api_key", "请输入 API Key。")
	}
	if f.service.launcher.BinaryPath == "" {
		return setupOperation{}, &requestDiagnostic{cause: ErrRequest, Message: "部署机缺少 DSH，请从完整发行目录运行 ./start。"}
	}
	if err := dsh.CheckConfig(dsh.Options{BinaryPath: f.service.launcher.BinaryPath, Model: input.ModelParameters.materialize()}); err != nil {
		return setupOperation{}, invalidField("model", "请检查协议、地址、模型 ID 与高级设置。")
	}
	data, _ := json.Marshal(input)
	digest := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(digest[:])
	if f.operation != nil {
		if f.operation.Operation.ID == input.RequestID {
			if f.operation.Digest != fingerprint {
				return setupOperation{}, ErrConflict
			}
			return f.operation.Operation, nil
		}
		if f.operation.Operation.Status == "validating" || f.operation.Operation.Status == "saved" {
			return setupOperation{}, ErrBusy
		}
	}
	if f.service.snapshot().Version != input.ExpectedVersion {
		return setupOperation{}, ErrConflict
	}
	ref, err := f.service.credentials.put(input.APIKey)
	if err != nil {
		return setupOperation{}, err
	}
	op := &savedSetupOperation{Operation: setupOperation{ID: input.RequestID, Digest: fingerprint, Status: "validating"}, Digest: fingerprint, Entry: modelEntry{ID: "daily", Name: input.ModelID, ModelParameters: input.ModelParameters.clone()}, Credential: ref, ExpectedVersion: input.ExpectedVersion}
	if err := f.persist(op); err != nil {
		_ = os.Remove(filepath.Join(f.service.credentials.dir, ref))
		return setupOperation{}, err
	}
	f.operation = op
	f.workers.Add(1)
	go f.validate(op)
	return op.Operation, nil
}
func (f *firstModelSetup) validate(op *savedSetupOperation) {
	defer f.workers.Done()
	ctx, cancel := context.WithTimeout(f.ctx, 90*time.Second)
	defer cancel()
	key, err := f.service.credentials.get(op.Credential)
	var catalog modelCatalog
	if err == nil {
		catalog, err = f.service.save(ctx, modelSave{Entry: op.Entry, APIKey: key.Value, ExpectedVersion: op.ExpectedVersion, MakeDefault: true})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		op.Operation.Status, op.Operation.Code, op.Operation.Message = "failed", "validation_failed", "模型未保存，请检查设置后重新验证。"
		var diagnostic *dsh.ProbeError
		if errors.As(err, &diagnostic) {
			op.Operation.Code = diagnostic.Stage + ":" + diagnostic.Code
			op.Operation.Message = "模型未保存：" + op.Operation.Code + "。请检查模型服务或设置后重试。"
		}
		if errors.Is(err, ErrConflict) {
			op.Operation.Code, op.Operation.Message = "conflict", "配置已被其他程序修改，请重启服务后重试。"
		}
	} else {
		entry, resolveErr := catalog.resolve(modelSelection{Mode: "default"})
		if resolveErr != nil || !reflect.DeepEqual(entry.ModelParameters.materialize(), op.Entry.ModelParameters.materialize()) {
			err = ErrConflict
		}
		if err != nil {
			op.Operation.Status, op.Operation.Code, op.Operation.Message = "failed", "conflict", "已保存的配置与目标不一致，请重启核对。"
		}
		if err == nil {
			op.Operation.Status, op.Operation.Message = "saved", "模型已通过文本和工具验证并保存，正在初始化助理。"
		}
	}
	// Publish completion before transitioning listeners. A crash after catalog
	// commit is recovered from the catalog and normal default creation receipt.
	if persistErr := f.persist(op); persistErr != nil {
		op.Operation.Status, op.Operation.Code, op.Operation.Message = "failed", "storage", "保存结果需要核对，请重启服务；已验证的配置会继续初始化。"
		return
	}
	_ = os.Remove(filepath.Join(f.service.credentials.dir, op.Credential))
	op.Credential = ""
	if op.Operation.Status == "saved" {
		select {
		case f.saved <- struct{}{}:
		default:
		}
	}
}

// No Room manager, platform worker or real business tools exist in setup mode.
func serveSetupRequired(ctx context.Context, listener net.Listener, config startupConfig, settings *systemSettings, access *serverAccess, ready func(any) error, stderr io.Writer, probe func(context.Context, dsh.Options) (dsh.ProbeResult, error)) (runErr error) {
	dataDir := *config.Server.DataDir
	if err := makeDurableDir(dataDir); err != nil {
		return err
	}
	lock, err := lockDirectory(dataDir)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, unlockDirectory(lock)) }()
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	setup, err := newFirstModelSetup(workerCtx, config, settings, probe)
	if err != nil {
		return err
	}
	defer func() { stop(); setup.workers.Wait() }()
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		Handler: access.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/" && r.Method == "GET" {
				http.Redirect(w, r, "/access", http.StatusSeeOther)
				return
			}
			if r.URL.Path == "/v1/setup" && r.Method == "GET" {
				_ = writeJSON(w, setup.state())
				return
			}
			if r.URL.Path == "/v1/setup/model" && r.Method == "POST" {
				var input setupModelInput
				dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
				dec.DisallowUnknownFields()
				if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
					accessError(w, 400, "invalid_request")
					return
				}
				op, err := setup.submit(input)
				input.APIKey = ""
				if err != nil {
					writeAPIError(w, err)
					return
				}
				w.WriteHeader(http.StatusAccepted)
				_ = writeJSON(w, op)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = writeJSON(w, map[string]any{"status": "setup_required", "ready": false})
		}))}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err := ready(struct {
		Endpoint string `json:"endpoint"`
		Status   string `json:"status"`
		Ready    bool   `json:"ready"`
	}{internalAccessEndpoint(listener.Addr().String()), "setup_required", false}); err != nil {
		return err
	}
	if access != nil && access.disabled {
		fmt.Fprintf(stderr, "打开 %s/access 配置并验证模型；当前仅本机访问，无需登录。\n", internalAccessEndpoint(listener.Addr().String()))
	} else {
		fmt.Fprintf(stderr, "打开 %s/access 登录并验证模型。管理 token 文件：%s\n", internalAccessEndpoint(listener.Addr().String()), adminTokenPath(config))
	}
	select {
	case <-ctx.Done():
	case <-setup.saved:
		err = errModelSetupSaved
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	return errors.Join(err, server.Shutdown(shutdownCtx))
}

func setupBlockedMessage(err error) string {
	if err == nil {
		return ""
	}
	return "已有数据需要模型迁移，当前版本尚未开放此实例的模型写入；请在部署机修复原配置。"
}

// The first-use write route remains idempotent after the listener transitions
// into the manager. It can only return the original accepted result.
func (m *roomManager) serveCompletedModelSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if m.config.models == nil {
		writeAPIError(w, ErrRequest)
		return
	}
	var input setupModelInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
		accessError(w, 400, "invalid_request")
		return
	}
	var op savedSetupOperation
	if readSnapshot(filepath.Join(filepath.Dir(m.settings.path), "model-setup.json"), &op, maxPrivateFileBytes) != nil || input.RequestID != op.Operation.ID {
		writeAPIError(w, ErrConflict)
		return
	}
	catalog := m.config.models.catalog()
	entry, err := catalog.resolve(modelSelection{Mode: "default"})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if input.Protocol == "" {
		input.Protocol = "openai-completions"
	}
	credential, err := m.config.models.Credentials.get(entry.CredentialRef)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if !reflect.DeepEqual(input.ModelParameters.materialize(), entry.materialize()) || (input.APIKey != "" && input.APIKey != credential.Value) {
		writeAPIError(w, ErrConflict)
		return
	}
	if input.APIKey == "" {
		input.APIKey = credential.Value
	}
	data, _ := json.Marshal(input)
	input.APIKey = ""
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != op.Digest || catalog.Version != op.ExpectedVersion+1 {
		writeAPIError(w, ErrConflict)
		return
	}
	op.Operation.Status = "saved"
	op.Operation.Message = "模型已验证保存；助理初始化状态请查看当前对话。"
	_ = writeJSON(w, op.Operation)
}
