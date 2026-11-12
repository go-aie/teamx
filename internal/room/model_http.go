package room

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sort"
)

type modelMemberView struct {
	MemberIdentity
	Selection      modelSelection   `json:"selection"`
	Version        uint64           `json:"version"`
	TargetEntryID  string           `json:"target_entry_id"`
	TargetRevision uint64           `json:"target_revision"`
	Actual         *modelActualView `json:"actual,omitempty"`
	OperationID    string           `json:"operation_id,omitempty"`
}
type modelActualView struct {
	EntryID    string `json:"entry_id"`
	Revision   uint64 `json:"revision"`
	ModelID    string `json:"model_id"`
	Generation string `json:"generation"`
	Pending    bool   `json:"pending"`
}

func (c *modelController) memberViewLocked(ref memberModelReference) modelMemberView {
	entry, _ := c.manager.config.models.catalog().resolve(ref.Selection)
	v := modelMemberView{MemberIdentity: ref.MemberIdentity, Selection: ref.Selection, Version: ref.Version, TargetEntryID: entry.ID, TargetRevision: entry.Revision, OperationID: c.manager.config.models.gate(ref.MemberIdentity)}
	path := filepath.Join(c.manager.roomDir(ref.RoomID), "agents", ref.MembershipID, "session.json")
	if reference, _, err := readDSHReference(path, ref.MemberIdentity); err == nil && reference.Directory != "" {
		var frozen modelSessionSnapshot
		if readSnapshot(filepath.Join(filepath.Dir(path), reference.Directory+".model.json"), &frozen, maxPrivateFileBytes) == nil && frozen.validate(ref.MemberIdentity) == nil {
			v.Actual = &modelActualView{frozen.EntryID, frozen.EntryRevision, frozen.Profile.Model.ModelID, reference.Directory, reference.Pending}
		}
	}
	return v
}
func (c *modelController) viewLocked(op *modelChangeOperation) any {
	type progress struct {
		MemberIdentity
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
		EntryID  string `json:"entry_id"`
		Revision uint64 `json:"revision"`
	}
	members := []progress{}
	completed, waiting, failed := 0, 0, 0
	for _, member := range op.Members {
		status := "waiting"
		if member.Error != "" {
			status = "failed"
		}
		if !member.Rotate && op.Committed && c.memberCompleted(member) {
			status = "completed"
		} else {
			if c.memberCompleted(member) {
				status = "completed"
			}
		}
		switch status {
		case "completed":
			completed++
		case "failed":
			failed++
		default:
			waiting++
		}
		members = append(members, progress{member.MemberIdentity, status, member.Error, member.Target.EntryID, member.Target.EntryRevision})
	}
	return struct {
		ID        string     `json:"id"`
		Status    string     `json:"status"`
		Message   string     `json:"message,omitempty"`
		Committed bool       `json:"committed"`
		Completed int        `json:"completed"`
		Waiting   int        `json:"waiting"`
		Failed    int        `json:"failed"`
		Members   []progress `json:"members"`
	}{op.ID, op.Status, op.Message, op.Committed, completed, waiting, failed, members}
}
func (m *roomManager) serveModels(w http.ResponseWriter, r *http.Request) {
	if m.modelControl == nil {
		writeAPIError(w, &requestDiagnostic{cause: ErrRequest, Message: "此实例尚未迁移到托管模型列表；现阶段不开放模型写入。"})
		return
	}
	c := m.modelControl
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		m.mu.Lock()
		defer m.mu.Unlock()
		refs, err := c.referencesLocked()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		members := []modelMemberView{}
		for _, ref := range refs {
			members = append(members, c.memberViewLocked(ref))
		}
		operations := []any{}
		operationIDs := make([]string, 0, len(c.operations))
		for id := range c.operations {
			operationIDs = append(operationIDs, id)
		}
		sort.Slice(operationIDs, func(i, j int) bool {
			a, b := c.operations[operationIDs[i]], c.operations[operationIDs[j]]
			if a.CreatedAt.Equal(b.CreatedAt) {
				return a.ID < b.ID
			}
			return a.CreatedAt.After(b.CreatedAt)
		})
		for _, id := range operationIDs {
			operations = append(operations, c.viewLocked(c.operations[id]))
		}
		type entryState struct {
			Validated            bool     `json:"validated"`
			CredentialConfigured bool     `json:"credential_configured"`
			Templates            []string `json:"templates"`
		}
		states := map[string]entryState{}
		for _, entry := range m.config.models.catalog().Entries {
			templates, err := c.templateReferencesLocked(entry.ID)
			if err != nil {
				writeAPIError(w, err)
				return
			}
			_, keyErr := c.service.credentials.get(entry.CredentialRef)
			var receipt modelValidation
			receiptErr := readSnapshot(filepath.Join(filepath.Dir(c.service.credentials.dir), "model-validations", entry.executionDigest()+".json"), &receipt, maxPrivateFileBytes)
			states[entry.ID] = entryState{receiptErr == nil && modelValidationMatches(entry, receipt), keyErr == nil, templates}
		}
		var migration modelMigration
		_ = readSnapshot(filepath.Join(filepath.Dir(c.service.credentials.dir), "model-migration.json"), &migration, maxRoomsFileBytes)
		migration.Files = nil // Targets and private backup paths never enter the response.
		_ = json.NewEncoder(w).Encode(struct {
			ServiceEpoch string                `json:"service_epoch"`
			Catalog      modelCatalog          `json:"catalog"`
			Members      []modelMemberView     `json:"members"`
			Operations   []any                 `json:"operations"`
			EntryStates  map[string]entryState `json:"entry_states"`
			Migration    any                   `json:"migration,omitempty"`
		}{m.epoch, m.config.models.catalog(), members, operations, states, struct {
			Status    string `json:"status"`
			Rooms     int    `json:"rooms"`
			Templates int    `json:"templates"`
		}{migration.Status, migration.Rooms, migration.Templates}})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/models/change" {
		var input modelChangeInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			writeAPIError(w, ErrRequest)
			return
		}
		if input.ServiceEpoch != m.epoch {
			writeAPIError(w, ErrIdentity)
			return
		}
		op, err := c.submit(input, requestOperator(r))
		input.APIKey = ""
		if err != nil {
			writeAPIError(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(op)
		return
	}
	if r.URL.Path == "/v1/models/operation" {
		if r.Method == http.MethodGet {
			m.mu.Lock()
			defer m.mu.Unlock()
			op := c.operations[r.URL.Query().Get("id")]
			if op == nil {
				writeAPIError(w, ErrRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(c.viewLocked(op))
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				ID           string `json:"id"`
				Action       string `json:"action"`
				ServiceEpoch string `json:"service_epoch"`
				Caller       Caller `json:"caller"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF || req.ServiceEpoch != m.epoch {
				writeAPIError(w, ErrRequest)
				return
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			op := c.operations[req.ID]
			if op == nil || op.Owner != requestOperator(r) {
				writeAPIError(w, ErrIdentity)
				return
			}
			if req.Action == "cancel" {
				if op.Status != "validating" {
					writeAPIError(w, ErrConflict)
					return
				}
				op.Status = "cancelled"
				op.Message = "已取消，未提交配置。"
				if err := c.persist(op); err != nil {
					writeAPIError(w, err)
					return
				}
			} else if req.Action == "retry" {
				if c.running[op.ID] {
					_ = json.NewEncoder(w).Encode(c.viewLocked(op))
					return
				}
				if op.Status == "completed" {
					_ = json.NewEncoder(w).Encode(c.viewLocked(op))
					return
				}
				if op.Target == nil {
					writeAPIError(w, ErrConflict)
					return
				}
				// Completed members are skipped by the durable rotation ID, including
				// after a restart between rotation completion and journal persistence.
				c.launchLocked(op, true)
			} else {
				writeAPIError(w, ErrRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(c.viewLocked(op))
			return
		}
	}
	writeAPIError(w, ErrRequest)
}
