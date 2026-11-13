package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

var errPlatformConnectNotFound = errors.New("connection operation not found")

func (m *roomManager) connectSessionLocked(id string) *larkAuthSession {
	for _, s := range m.larkAuthSessions {
		if s.operationID == id {
			return s
		}
	}
	return nil
}
func (m *roomManager) publicPlatformConnectLocked(op platformConnectOperation) (platformConnectOperation, error) {
	if op.Input.Provider == "gitlab" && op.ApplicationFingerprint == "" && !connectTerminal(op.Status) {
		var err error
		op.Setup, err = m.gitlabConnectSetupLocked(op)
		if err != nil {
			return op, err
		}
	}
	v, err := m.connectContinuationLocked(op)
	if err != nil {
		return op, err
	}
	if v.Mode == "inbox" || v.HandledVersion > 0 {
		op.Continuation = &v
	} else if _, err := os.Lstat(m.connectNotificationPath(op.ID)); err == nil {
		// Explicitly disabled policy is observable too. Historical operations
		// without a policy retain their original response shape.
		op.Continuation = &v
	} else if !errors.Is(err, os.ErrNotExist) {
		return op, err
	}
	if s := m.gitlabConnectSessionLocked(op.ID); s != nil && s.connectErr != nil {
		return op, s.connectErr
	}
	if s := m.connectSessionLocked(op.ID); s != nil {
		if s.connectErr != nil {
			return op, s.connectErr
		}
		if !connectTerminal(op.Status) && !op.CancelRequested {
			v := m.publicLarkAuthView(s)
			op.Interaction.URL, op.Interaction.QRCodeURL = v.URL, v.QRCodeURL
		}
	}
	return op, nil
}
func (m *roomManager) checkpointPlatformConnectLocked(s *larkAuthSession) {
	if s.operationID == "" {
		return
	}
	op, err := m.readPlatformConnectLocked(s.operationID)
	if err != nil {
		s.connectErr = err
		s.cancel()
		return
	}
	if connectTerminal(op.Status) || op.CancelRequested {
		return
	}
	op.Stage = s.Stage
	op.Status = "running"
	if s.Status == "waiting" {
		op.Status = "waiting"
	}
	if s.ConfigurationFound {
		if connectIntent(op.Input) != "authorize" {
			op.Application = onboardingStep{Status: "applied"}
		}
		p := s.profile
		op.Profile = &p
	}
	if err = m.savePlatformConnectLocked(&op); err != nil {
		s.connectErr = err
		s.cancel()
	}
}

// Called under mu after the native process has settled. The session reservation
// and the operation's cancellation fence cover the entire connection commit.
func (m *roomManager) finishPlatformConnectLocked(s *larkAuthSession) {
	if s.operationID == "" {
		return
	}
	op, err := m.readPlatformConnectLocked(s.operationID)
	if err != nil {
		s.connectErr = err
		return
	}
	if connectTerminal(op.Status) {
		return
	}
	if s.ConfigurationFound {
		if connectIntent(op.Input) != "authorize" {
			op.Application = onboardingStep{Status: "applied"}
		}
		p := s.profile
		op.Profile = &p
	}
	cancelled := op.CancelRequested
	if cancelled {
		op.Status, op.ErrorCode = "cancelled", "cancelled"
	}
	if !cancelled {
		op.Status, op.ErrorCode = s.Status, s.ErrorCode
		switch s.Status {
		case "succeeded":
			op.Status = "running"
		case "failed", "cancelled", "expired":
		default:
			op.Status = "unknown"
		}
	}
	if s.validation.Status == "valid" && (connectIntent(op.Input) != "authorize" || s.validation.Account == op.Account) {
		op.Authorization = onboardingStep{Status: "applied"}
		if connectIntent(op.Input) != "authorize" {
			op.Account = s.validation.Account
		}
	}
	if op.Schema == 2 && s.Permissions != nil && s.Permissions.Account == op.Account {
		op.Permissions = s.Permissions
	}
	if op.Status == "running" {
		// Check the original membership again after the long authorization wait.
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = m.checkOnboardingTargetLocked(checkCtx, platformOnboardingSpec{MemberIdentity: op.MemberIdentity})
		cancel()
		if m.closing {
			err = ErrClosing
		}
		if err != nil {
			op.Status, op.ErrorCode = "failed", "target_changed"
		} else {
			op.Stage = "connection"
			// Persist exact account/profile evidence before touching connection state.
			err = m.savePlatformConnectLocked(&op)
			if err == nil {
				if connectIntent(op.Input) == "authorize" {
					err = m.commitLarkAuthorizeLocked(&op, s.validation)
				} else {
					err = m.commitPlatformConnectLocked(&op, s.validation)
				}
			}
			if err != nil {
				op.Status, op.ErrorCode = "unknown", "connection_unconfirmed"
			} else {
				op.Status, op.Stage, op.ErrorCode = "completed", "complete", ""
			}
		}
	}
	if op.Status == "failed" || op.Status == "expired" {
		if s.Stage == "application" && op.Application.Status == "pending" {
			op.Application = onboardingStep{Status: "failed", ErrorCode: op.ErrorCode}
		}
		if s.Stage == "authorization" && op.Authorization.Status == "pending" {
			op.Authorization = onboardingStep{Status: "failed", ErrorCode: op.ErrorCode}
		}
	}
	if op.Status == "unknown" && op.Stage == "connection" {
		op.Connection = onboardingStep{Status: "unknown", ErrorCode: op.ErrorCode}
	}
	op.ResultVersion++
	if saveErr := m.savePlatformConnectLocked(&op); saveErr != nil {
		s.connectErr = saveErr
	}
	m.wakeConnectNotificationsLocked()
}
func (m *roomManager) commitPlatformConnectLocked(op *platformConnectOperation, validation platform.Validation) error {
	// New-only target and fresh profile have no shared references to rotate.
	file, err := m.platforms.Load()
	if err != nil {
		return err
	}
	for _, c := range file.Platforms {
		if c.ID == op.PlatformID || c.Provider == "lark" && c.CLIProfile != nil && op.Profile != nil && c.CLIProfile.Profile == op.Profile.Profile {
			return ErrConflict
		}
	}
	change, err := m.platforms.Plan(registry.Request{ID: op.PlatformID, Name: op.Input.Name, Provider: "lark", URL: lark.Origin("feishu"), CLIProfile: op.Profile}, false)
	if err != nil {
		return managedPlatformError(err)
	}
	c, err := m.platforms.Commit(change)
	if err != nil {
		return err
	}
	ticket, err := m.platforms.BeginVerification(c.ID, c.Version)
	if err != nil {
		return err
	}
	c, err = m.platforms.CompleteVerification(ticket, validation)
	if err != nil {
		return err
	}
	op.Connection = onboardingStep{Status: "connected", Version: c.Version}
	return m.applyLarkAccountCheckLocked(*op.Profile, validation.Account, op.Permissions)
}
func (m *roomManager) startPlatformConnectLocked(ctx context.Context, req platformConnectRequest) (platformConnectOperation, error) {
	var op platformConnectOperation
	if req.Input == nil || !tool.OpaqueID(req.OperationID) || req.SourceSeq == 0 || (req.Input.Provider != "lark" && req.Input.Provider != "gitlab") || (connectIntent(*req.Input) != "authorize" && !validText(req.Input.Name)) || len(req.Input.Name) > 128 {
		return op, ErrRequest
	}
	if connectIntent(*req.Input) == "authorize" && !validConnectInput(*req.Input) {
		return op, ErrRequest
	}
	prior, err := m.readPlatformConnectLocked(req.OperationID)
	if err == nil {
		if prior.MemberIdentity != (MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}) || prior.Caller != req.Caller {
			return op, ErrIdentity
		}
		if connectIntent(*req.Input) == "authorize" && req.Input.Name == "" {
			input := *req.Input
			input.Name = prior.Input.Name
			req.Input = &input
		}
		if prior.Source.Seq != req.SourceSeq || prior.Input != *req.Input {
			return op, ErrConflict
		}
		return m.publicPlatformConnectLocked(prior)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return op, err
	}
	if connectIntent(*req.Input) == "authorize" && req.Input.Name == "" {
		file, err := m.platforms.Load()
		if err != nil {
			return op, err
		}
		input := *req.Input
		for _, c := range file.Platforms {
			if c.ID == input.PlatformID {
				input.Name = c.Name
			}
		}
		if input.Name == "" {
			return op, ErrConflict
		}
		req.Input = &input
	}
	name, _, normalizeErr := registry.Normalize(req.Input.Name, connectOrigin(*req.Input))
	if normalizeErr != nil || name != req.Input.Name || !validConnectInput(*req.Input) {
		return op, ErrRequest
	}

	if _, e := m.readPlatformSelectionLocked(req.OperationID); !errors.Is(e, os.ErrNotExist) {
		if e == nil {
			return op, ErrConflict
		}
		return op, e
	}
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	if svc == nil {
		return op, ErrIdentity
	}
	snapshot, err := svc.store.LoadRoom()
	if err != nil {
		return op, err
	}
	source, err := roomHumanMessage(snapshot, req.SourceSeq)
	if err != nil {
		return op, ErrIdentity
	}
	ops, err := m.platformConnectOperationsLocked()
	if err != nil {
		return op, err
	}
	if len(ops) >= 1000 {
		return op, ErrCapacity
	}
	// One original request has one connection operation. Lost responses recover by
	// source_seq; changing the operation ID never starts another authorization.
	for _, p := range ops {
		if p.MemberIdentity == (MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}) && p.Source.Seq == req.SourceSeq && connectIntent(p.Input) == connectIntent(*req.Input) && (connectIntent(p.Input) != "authorize" || p.Input.PlatformID == req.Input.PlatformID) {
			return op, conflictReason("source_seq", "connection operation already exists", "Query status with the original source_seq.")
		}
	}
	id, err := newOpaqueID()
	if err != nil {
		return op, err
	}
	op = platformConnectOperation{Schema: 1, ID: req.OperationID, MemberIdentity: MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}, Caller: req.Caller, Source: source, Input: *req.Input, Status: "running", Stage: "application", PlatformID: id, Application: onboardingStep{Status: "pending"}, Authorization: onboardingStep{Status: "pending"}, Connection: onboardingStep{Status: "pending"}, Interaction: platformConnectInteraction{ID: req.OperationID, Kind: "lark_authorization"}, CreatedAt: time.Now().UTC()}
	if req.Input.Provider == "gitlab" {
		return m.startGitLabConnectLocked(op)
	}
	op.Schema = 2
	if connectIntent(op.Input) == "authorize" {
		return m.startLarkAuthorizeLocked(ctx, op)
	}
	if req.Input.PlatformID != "" {
		file, e := m.platforms.Load()
		if e != nil {
			return op, e
		}
		for _, c := range file.Platforms {
			if c.ID == req.Input.PlatformID && c.Version == req.Input.PlatformVersion && c.Provider == "lark" && c.Name == req.Input.Name {
				a, _, e := registry.ResolveAccessAccount(file, c)
				if e != nil {
					return op, ErrConflict
				}
				op.PlatformID = c.ID
				op.Account = a.Identity
				op.Stage = "authorization"
				op.Application.Status, op.Authorization.Status = "reused", "reused"
				op.Connection = onboardingStep{Status: "connected", Version: c.Version}
				if err := m.savePlatformConnectLocked(&op); err != nil {
					return op, err
				}
				checkCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				call := &platformCall{operationID: op.ID, owner: op.RoomIdentity, platformID: c.ID, agentID: op.AgentID, membershipID: op.MembershipID, tool: "lark", cancel: cancel, done: make(chan struct{})}
				if m.platformCalls == nil {
					m.platformCalls = map[*platformCall]bool{}
				}
				m.platformCalls[call] = true
				go m.checkReusedLarkConnection(checkCtx, op, c, call)
				return op, nil
			}
		}
		return op, ErrConflict
	}
	if req.Input.PlatformVersion != 0 {
		return op, ErrRequest
	}
	// Resolve the official executable server-side. Agent input cannot choose code
	// to execute, an existing profile to overwrite, or credentials.
	s, runCtx, err := m.prepareLarkSetupLocked(larkAuthRequest{Caller: Caller{Kind: "human", ID: source.Author.ID}, Brand: "feishu"})
	if err != nil {
		return op, err
	}
	s.operationID = op.ID
	if err = m.savePlatformConnectLocked(&op); err != nil {
		s.cancel()
		close(s.done)
		delete(m.larkAuthSessions, s.SessionID)
		return op, err
	}
	go m.runLarkSetup(runCtx, s, "", "feishu")
	return op, nil
}
func (m *roomManager) servePlatformConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	var req platformConnectRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/v1/platforms/connect/")
	if r.URL.Path == "/v1/platforms/skill" {
		action = "skill"
	}
	if r.URL.Path == "/v1/platforms/list" {
		action = "list"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkToolMemberLocked(r.Context(), toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.Notify != nil && (action != "start" && action != "authorize" && action != "notify" || *req.Notify != "inbox" && *req.Notify != "none") || req.ResultVersion != 0 && action != "acknowledge" {
		writeAPIError(w, ErrRequest)
		return
	}
	if action == "skill" || action == "list" {
		if req.Input != nil || req.OperationID != "" || req.SourceSeq != 0 {
			writeAPIError(w, ErrRequest)
			return
		}
		if action == "skill" {
			data, err := tool.ReadSkill(tool.Definition{Skill: "skills/platforms/SKILL.md"})
			if err != nil {
				writeAPIError(w, err)
				return
			}
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		type connection struct {
			ID           string                        `json:"id"`
			Name         string                        `json:"name"`
			Provider     string                        `json:"provider"`
			Version      uint64                        `json:"version"`
			Account      platform.AccountIdentity      `json:"account"`
			Connected    bool                          `json:"connected"`
			Origin       string                        `json:"origin"`
			Username     string                        `json:"username,omitempty"`
			Capabilities *larkPlatformCapabilityResult `json:"capabilities,omitempty"`
		}
		file, err := m.platforms.Load()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		list := []connection{}
		for _, c := range file.Platforms {
			if c.Provider != "lark" && c.Provider != "gitlab" {
				continue
			}
			v := connection{ID: c.ID, Name: c.Name, Provider: c.Provider, Version: c.Version, Origin: c.URL, Username: c.Validation.Username}
			if c.Provider == "lark" {
				p := m.larkPlatformCapabilitiesLocked(c)
				v.Capabilities = &p
			}
			if a, _, e := registry.ResolveAccessAccount(file, c); e == nil {
				v.Account, v.Connected = a.Identity, true
			}
			list = append(list, v)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Platforms    []connection               `json:"platforms"`
			Applications []gitlabConnectApplication `json:"gitlab_applications"`
		}{list, m.gitlabConnectApplicationsLocked(file)})
		return
	}
	if (req.Intent != "" || req.PlatformID != "") && (action != "status" || req.SourceSeq == 0 || req.OperationID != "" || req.Intent != "authorize" || !tool.OpaqueID(req.PlatformID)) {
		writeAPIError(w, ErrRequest)
		return
	}
	var op platformConnectOperation
	var err error
	switch action {
	case "authorize":
		if req.Input == nil || req.Input.Intent != "authorize" {
			err = ErrRequest
			break
		}
		op, err = m.startPlatformConnectLocked(r.Context(), req)
	case "start":
		if req.Input != nil && connectIntent(*req.Input) != "connect" {
			err = ErrRequest
			break
		}
		op, err = m.startPlatformConnectLocked(r.Context(), req)
	case "status", "cancel", "notify", "acknowledge":
		if req.Input != nil || req.OperationID != "" && req.SourceSeq != 0 || req.OperationID == "" && req.SourceSeq == 0 {
			err = ErrRequest
			break
		}
		if req.OperationID != "" {
			op, err = m.readPlatformConnectLocked(req.OperationID)
		} else {
			var ops []platformConnectOperation
			ops, err = m.platformConnectOperationsLocked()
			if err == nil {
				err = errPlatformConnectNotFound
				for _, p := range ops {
					if p.RoomIdentity == req.RoomIdentity && p.Caller == req.Caller && p.Source.Seq == req.SourceSeq && connectIntent(p.Input) == connectIntent(platformConnectInput{Intent: req.Intent}) && (req.Intent != "authorize" || p.Input.PlatformID == req.PlatformID) {
						op, err = p, nil
						break
					}
				}
			}
		}
		if err == nil && (op.RoomIdentity != req.RoomIdentity || op.Caller != req.Caller) {
			err = ErrIdentity
		}
		if err == nil && action == "cancel" {
			op, err = m.cancelPlatformConnectLocked(r.Context(), op)
		}
		if err == nil && action == "acknowledge" {
			err = m.acknowledgeConnectLocked(op, req.ResultVersion)
		}
	default:
		err = ErrRequest
	}
	if err == nil && action == "notify" && req.Notify == nil {
		err = ErrRequest
	}
	if err == nil && req.Notify != nil {
		err = m.setConnectNotificationLocked(op, *req.Notify)
		if err == nil && *req.Notify == "inbox" {
			m.startConnectNotificationsLocked()
			m.wakeConnectNotificationsLocked()
		}
	}
	if err == nil {
		op, err = m.publicPlatformConnectLocked(op)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errPlatformConnectNotFound) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(apiError{Error: "connection operation not found", Code: "operation_not_found", Message: "No connection operation matches this member and request."})
			return
		}
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(op)
}

// The configuration lock is held on entry and return. Both Agent commands and
// human interaction controls use the same durable cancellation fence.
func (m *roomManager) cancelPlatformConnectLocked(ctx context.Context, op platformConnectOperation) (platformConnectOperation, error) {
	if connectTerminal(op.Status) {
		return op, nil
	}
	if !op.CancelRequested {
		op.CancelRequested, op.Status = true, "cancelling"
		if err := m.savePlatformConnectLocked(&op); err != nil {
			return op, err
		}
	}
	if op.Input.Provider == "gitlab" {
		if s := m.gitlabConnectSessionLocked(op.ID); s != nil {
			m.cancelGitLabAuthLocked(s)
		} else {
			op.Status, op.ErrorCode, op.ResultVersion = "cancelled", "cancelled", 1
			if err := m.savePlatformConnectLocked(&op); err != nil {
				return op, err
			}
			m.wakeConnectNotificationsLocked()
		}
		return m.readPlatformConnectLocked(op.ID)
	}
	if s := m.connectSessionLocked(op.ID); s != nil {
		s.cancel()
		m.mu.Unlock()
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-s.done:
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
		m.mu.Lock()
		return m.readPlatformConnectLocked(op.ID)
	}
	pending := m.cancelPlatformCallsLocked(func(c *platformCall) bool { return c.operationID == op.ID })
	if err := m.settlePlatformCallsLocked(pending); err != nil {
		return op, err
	}
	op.Status, op.ErrorCode, op.ResultVersion = "cancelled", "cancelled", 1
	if err := m.savePlatformConnectLocked(&op); err != nil {
		return op, err
	}
	m.wakeConnectNotificationsLocked()
	return op, nil
}
