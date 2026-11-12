package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/pelletier/go-toml/v2"
)

// Caller holds manager.mu. Include unopened rooms and persisted templates;
// historical generation snapshots deliberately do not prevent deletion.
func (c *modelController) templateReferencesLocked(id string) ([]string, error) {
	var refs []string
	for _, team := range c.manager.teams {
		for _, member := range team.Members {
			if member.ModelSelection != nil && member.ModelSelection.Mode == "explicit" && member.ModelSelection.EntryID == id {
				refs = append(refs, team.ID+" / "+member.ID)
			}
		}
	}
	if c.manager.teamsDir == "" {
		return refs, nil
	}
	files, err := os.ReadDir(c.manager.teamsDir)
	if os.IsNotExist(err) {
		return refs, nil
	}
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if !file.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(c.manager.teamsDir, file.Name(), "team.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || int64(len(data)) > maxPrivateFileBytes {
			return nil, ErrInvalid
		}
		var team teamDefinition
		if json.Unmarshal(data, &team) != nil {
			return nil, ErrInvalid
		}
		for _, member := range team.Members {
			if member.ModelSelection != nil && member.ModelSelection.Mode == "explicit" && member.ModelSelection.EntryID == id {
				refs = append(refs, team.ID+" / "+member.ID)
			}
		}
	}
	return refs, nil
}

func (c *modelController) commitCatalogLocked(before, next modelCatalog) error {
	if next.Version == ^uint64(0) {
		return ErrCapacity
	}
	next.Version = before.Version + 1
	if next.Version == 0 {
		return ErrCapacity
	}
	if err := next.validate(); err != nil {
		return err
	}
	publish, err := c.manager.config.models.publication(next)
	if err != nil {
		return err
	}
	s := c.service
	s.document.mu.Lock()
	defer s.document.mu.Unlock()
	return s.document.commitLocked(func(source []byte) ([]byte, func(), error) {
		var disk startupConfig
		if toml.Unmarshal(source, &disk) != nil || disk.Models == nil || !reflect.DeepEqual(*disk.Models, before) || !reflect.DeepEqual(s.current, before) {
			return nil, nil, ErrConflict
		}
		patched, err := patchModelCatalog(source, next)
		return patched, func() { s.current = next.clone(); publish() }, err
	})
}

func (c *modelController) deleteModel(ctx context.Context, op *modelChangeOperation) error {
	before := c.service.snapshot()
	entry, err := before.resolve(modelSelection{Mode: "explicit", EntryID: op.Input.EntryID})
	if err != nil {
		return err
	}
	c.manager.mu.Lock()
	refs, err := c.referencesLocked()
	templates, templateErr := c.templateReferencesLocked(entry.ID)
	c.manager.mu.Unlock()
	if err != nil {
		return err
	}
	if templateErr != nil {
		return templateErr
	}
	if len(templates) != 0 {
		return errModelTemplateReference
	}
	used := before.Default == entry.ID
	for _, ref := range refs {
		used = used || ref.Selection.Mode == "explicit" && ref.Selection.EntryID == entry.ID
	}
	next := before.clone()
	if used || op.Input.ReplacementID != "" {
		if op.Input.ReplacementID == entry.ID {
			return ErrInvalid
		}
		replacement, err := before.resolve(modelSelection{Mode: "explicit", EntryID: op.Input.ReplacementID})
		if err != nil {
			return err
		}
		// This operation confirms the current replacement, never trusting an old receipt.
		opts := c.service.launcher
		opts.Model = replacement.materialize()
		opts.Credential, err = c.service.credentials.get(replacement.CredentialRef)
		if err != nil {
			return err
		}
		receipt, err := c.service.probe(ctx, opts)
		if err != nil {
			return err
		}
		if !receipt.Text || !receipt.ToolRoundTrip || !receipt.Cleanup || receipt.Identity.Version == "" || receipt.Identity.Agent == (dsh.Artifact{}) {
			return ErrInvalid
		}
		if err := saveModelValidation(c.service.credentials.dir, modelValidation{replacement.ID, replacement.Revision, replacement.executionDigest(), replacement.CredentialRef, receipt}); err != nil {
			return err
		}
		if next.Default == entry.ID {
			next.Default = replacement.ID
		}
		for i := range next.Entries {
			if next.Entries[i].ID == replacement.ID {
				next.Entries[i].Unverified = false
			}
		}
	}
	for i := range next.Entries {
		if next.Entries[i].ID == entry.ID {
			next.Entries[i].PendingDeletion = true
		}
	}
	next.Version++
	c.manager.mu.Lock()
	defer c.manager.mu.Unlock()
	if op.Status == "cancelled" {
		return context.Canceled
	}
	if c.manager.config.models.catalog().Version != op.Input.ExpectedVersion {
		return ErrConflict
	}
	templates, err = c.templateReferencesLocked(entry.ID)
	if err != nil {
		return err
	}
	if len(templates) != 0 {
		return errModelTemplateReference
	}
	if err := c.prepareLocked(ctx, op, before, next); err != nil {
		return err
	}
	if err := c.commitCatalogLocked(before, next); err != nil {
		return err
	}
	op.Committed, op.Status = true, "applying"
	return c.persist(op)
}

func (c *modelController) replaceDeletedSelectionLocked(member modelChangeMember) (bool, error) {
	refs, err := c.referencesLocked()
	if err != nil {
		return false, err
	}
	for _, ref := range refs {
		if ref.MemberIdentity != member.MemberIdentity {
			continue
		}
		// Compare and swap only the original reference. A later selection is never overwritten.
		return true, c.saveSelectionLocked(member)
	}
	return false, nil // exact membership removed
}

func (c *modelController) finishDeletionLocked(op *modelChangeOperation) error {
	before := c.manager.config.models.catalog()
	found := false
	for _, entry := range before.Entries {
		if entry.ID == op.Input.EntryID {
			found = true
			if !entry.PendingDeletion {
				return ErrConflict
			}
		}
	}
	if !found {
		return nil
	} // crash after removal, before operation completion
	refs, err := c.referencesLocked()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.Selection.Mode == "explicit" && ref.Selection.EntryID == op.Input.EntryID {
			return ErrBusy
		}
	}
	templates, err := c.templateReferencesLocked(op.Input.EntryID)
	if err != nil {
		return err
	}
	if len(templates) != 0 {
		return errModelTemplateReference
	}
	for _, other := range c.operations {
		for _, member := range other.Members {
			if other.ID != op.ID && member.Target.EntryID == op.Input.EntryID && c.manager.config.models.gate(member.MemberIdentity) == other.ID {
				return ErrBusy
			}
		}
	}
	next := before.clone()
	next.Entries = nil
	for _, entry := range before.Entries {
		if entry.ID != op.Input.EntryID {
			next.Entries = append(next.Entries, entry)
		}
	}
	return c.commitCatalogLocked(before, next)
}
