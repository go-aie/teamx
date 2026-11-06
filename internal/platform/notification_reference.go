package platform

import (
	"strings"
	"unicode/utf8"
)

func (r NotificationReference) Valid() bool {
	if r.Version == 0 {
		return r == (NotificationReference{})
	}
	if r.Version != 1 && r.Version != 2 {
		return false
	}
	if r.Version == 1 && (r.ChatID != (ExternalID{}) || r.ParentID != (ExternalID{}) || r.RootID != (ExternalID{}) || r.AccountTenant != (ExternalID{})) {
		return false
	}
	if r.Version == 2 && (!r.ChatID.Valid() || !r.Target.ID.Valid() || r.Target.Type != "message" || !r.RecipientID.Valid() || !r.AccountTenant.Valid()) {
		return false
	}
	for _, id := range []ExternalID{r.ChatID, r.ParentID, r.RootID, r.AccountTenant, r.RecipientID, r.ProjectID, r.Target.ID, r.Target.IID, r.Object.ID, r.Object.IID, r.NoteID, r.DiscussionID} {
		if id == (ExternalID{}) {
			continue
		}
		if strings.TrimSpace(id.Namespace) == "" || strings.TrimSpace(id.Value) == "" ||
			!referenceText(id.Namespace) || !referenceText(id.Value) {
			return false
		}
	}
	for _, object := range []ObjectReference{r.Target, r.Object} {
		if object.Type == "" && object != (ObjectReference{}) {
			return false
		}
	}
	for _, s := range []string{r.Scope, r.Target.Type, r.Object.Type, r.Branch, r.Ref, r.RefType, r.SHA, r.BeforeSHA} {
		if !referenceText(s) {
			return false
		}
	}
	return true
}

func referenceText(s string) bool {
	return len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
