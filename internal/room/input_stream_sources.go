package room

import (
	"errors"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"os"
	"path/filepath"
	"sort"
)

func inputSourceRef(e InboxEntry) string {
	if e.sourceRef != "" {
		return e.sourceRef
	}
	if e.Location != nil {
		return referenceDigest("src_", e.MemberIdentity, *e.Location, e.Item.DeliveryID)
	}
	return referenceDigest("src_", e.MemberIdentity, e.Seq, e.Item.DeliveryID)
}
func (s *Store) inputEventPath(e InboxEntry) string {
	if e.Location != nil {
		return filepath.Join(s.followupRoot(e.MemberIdentity), "input-sources", inputSourceRef(e)+".json")
	}
	return s.eventPath(e.MemberIdentity, e.Seq)
}

// Caller holds Store.mu; Repository never calls back into the Store lock.
func (s *Store) businessInputLocked(id MemberIdentity, seq uint64, l *inputstream.Location) (InboxEntry, error) {
	if l == nil {
		return s.inboxEntryLocked(id, seq)
	}
	p := s.platformInputs[id.MembershipID]
	if p == nil || p.id != id {
		return InboxEntry{}, ErrIdentity
	}
	if shared := s.sharedHandle.Load(); shared != nil && l.Partition == shared.view.Load().snapshot.Partition {
		return shared.view.Load().entry(id, *l)
	}
	provider, snap, err := p.repository.Locate(*l)
	if err != nil {
		return InboxEntry{}, err
	}
	rec, err := p.repository.Read(snap, *l)
	if err != nil {
		return InboxEntry{}, err
	}
	cap := s.notificationCapabilities(provider).Input
	if cap == nil || cap.DecodeStoredInput == nil {
		return InboxEntry{}, inputstream.ErrUnsupported
	}
	item, err := cap.DecodeStoredInput(rec)
	if err != nil {
		return InboxEntry{}, err
	}
	ref, err := p.repository.DeliverySourceRef(*l, rec.Channel, rec.DeliveryID)
	if err != nil {
		return InboxEntry{}, err
	}
	return InboxEntry{Version: formatVersion, MemberIdentity: id, Location: l, Seq: l.Seq, Item: item, ReceivedAt: rec.ReceivedAt, sourceRef: ref}, nil
}
func (s *Store) authorizeInputLocationLocked(id MemberIdentity, turn uint64, l inputstream.Location) error {
	p := s.platformInputs[id.MembershipID]
	if p == nil || p.id != id {
		return ErrIdentity
	}
	e, err := p.executionLocked(turn)
	if err != nil {
		return err
	}
	state, err := p.repository.State(e)
	if err != nil {
		return err
	}
	for _, a := range state.Attempts {
		if a.Phase == "failed_before_output" {
			continue
		}
		for _, candidate := range a.Entries {
			if candidate == l {
				return nil
			}
		}
	}
	return ErrIdentity
}

func reclaimedInputSourceProof(link followupEvent) string {
	return referenceDigest("reclaimed_source_", link.MemberIdentity, *link.Location, link.Seq, link.DeliveryID, link.SourceRef, link.Source)
}

// source_ref is the only new Agent locator. Missing retained bodies are errors;
// only a committed reclamation boundary yields an expired body.
func (s *Store) inputSourceContentLocked(id MemberIdentity, ref string) (followupEvent, *InboxEntry, error) {
	var link followupEvent
	if !validOpaqueRef(ref, "src_") {
		return link, nil, ErrRequest
	}
	if err := readSnapshot(filepath.Join(s.followupRoot(id), "input-sources", ref+".json"), &link, followupBytes); err != nil {
		return link, nil, err
	}
	if link.Location == nil || link.MemberIdentity != id || link.SourceRef != ref {
		return link, nil, ErrIdentity
	}
	entry, err := s.businessInputLocked(id, 0, link.Location)
	if errors.Is(err, inputstream.ErrExpired) {
		entry = InboxEntry{MemberIdentity: id, Location: link.Location, Seq: link.Location.Seq}
		entry.Item.DeliveryID = link.DeliveryID
		entry.Item.Source = link.Source
		if !validText(link.DeliveryID) || !validIntegrationSource(link.Source) {
			return link, nil, ErrInvalid
		}
		p := s.platformInputs[id.MembershipID]
		provider, _, err := p.repository.Locate(*link.Location)
		if err != nil {
			return link, nil, err
		}
		if provider != link.Source.Provider {
			return link, nil, ErrIdentity
		}
		entry.sourceRef, err = p.repository.DeliverySourceRef(*link.Location, link.Source.Channel, link.DeliveryID)
		if errors.Is(err, inputstream.ErrExpired) {
			if link.ReclaimedSourceProof != reclaimedInputSourceProof(link) {
				return link, nil, ErrInvalid
			}
			entry.sourceRef = link.SourceRef
		} else if err != nil {
			return link, nil, err
		}
		validated, err := s.validateFollowupEventLocked(entry, link, link.ContextOnly)
		return validated, nil, err
	}
	if err != nil {
		return link, nil, err
	}
	validated, err := s.validateFollowupEventLocked(entry, link, inboxContextOnly(entry))
	return validated, &entry, err
}
func (s *Store) visitInputSourcesLocked(id MemberIdentity, visit func(followupEvent) error) error {
	paths, err := os.ReadDir(filepath.Join(s.followupRoot(id), "input-sources"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, path := range paths {
		name := path.Name()
		if path.IsDir() || len(name) < 5 || filepath.Ext(name) != ".json" {
			return ErrInvalid
		}
		ref := name[:len(name)-5]
		link, _, err := s.inputSourceContentLocked(id, ref)
		if err != nil {
			return err
		}
		if err := visit(link); err != nil {
			return err
		}
	}
	return nil
}

// Source pagination uses stable member-scoped references. A platform Seq is not
// a cursor across partitions and is never exposed by this new-format endpoint.
type inputSourcePage struct {
	Items    []inputStreamSource `json:"items"`
	Next     string              `json:"next_cursor,omitempty"`
	Complete bool                `json:"complete"`
}

func (s *Store) listInputSources(id MemberIdentity, objectRef, cursor string, limit int) (inputSourcePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := inputSourcePage{Items: []inputStreamSource{}}
	if err := s.checkFollowupsLocked(id, false); err != nil {
		return out, err
	}
	if limit < 1 || limit > 25 {
		return out, ErrRequest
	}
	links := []followupEvent{}
	if err := s.visitInputSourcesLocked(id, func(link followupEvent) error {
		if objectRef == "" || link.ObjectRef == objectRef {
			links = append(links, link)
		}
		return nil
	}); err != nil {
		return out, err
	}
	sort.Slice(links, func(i, j int) bool { return links[i].SourceRef < links[j].SourceRef })
	offset := 0
	if cursor != "" {
		found := false
		for i, link := range links {
			if inputstream.Ref("source_cursor_", id, objectRef, link.SourceRef) == cursor {
				offset = i + 1
				found = true
				break
			}
		}
		if !found {
			return out, inputstream.ErrExpired
		}
	}
	for i := offset; i < len(links); i++ {
		link := links[i]
		if len(out.Items) == limit {
			out.Next = inputstream.Ref("source_cursor_", id, objectRef, links[i-1].SourceRef)
			return out, nil
		}
		v := projectStreamNotification(inboxEntryView{Source: sourceSummary(link)}).Source
		out.Items = append(out.Items, v)
	}
	out.Complete = true
	return out, nil
}
func projectInputSource(link followupEvent, entry *InboxEntry) any {
	var v *inputStreamNotificationEntry
	status := "expired"
	if entry != nil {
		p := projectStreamNotification(projectInboxEntry(*entry, sourceSummary(link)))
		p.Item.Receiver = platform.AccountIdentity{}
		p.Item.Tenant = platform.ExternalID{}
		p.Item.ProjectID = 0
		v = &p
		status = "retained"
	}
	return struct {
		Source inputStreamSource             `json:"source"`
		Entry  *inputStreamNotificationEntry `json:"entry,omitempty"`
		Status string                        `json:"status"`
	}{projectStreamNotification(inboxEntryView{Source: sourceSummary(link)}).Source, v, status}
}
