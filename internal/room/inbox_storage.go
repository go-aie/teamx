package room

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform/notification"
)

type inboxHeader struct {
	Version int `json:"version"`
	MemberIdentity
	Limits InboxLimits `json:"limits"`
}

type inboxIndex struct {
	seq    uint64
	digest [32]byte
}

func inboxEntryName(seq uint64) string { return fmt.Sprintf("%020d.json", seq) }

func inboxEntrySize(entry InboxEntry) (int64, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // Match Store.save, including its trailing newline.
	err := enc.Encode(entry)
	return int64(b.Len()), err
}

func inboxDigest(item notification.Item) [32]byte {
	// Supplemental presentation snapshots do not change a delivery's original
	// identity. This also preserves receipts made before snapshots were added.
	item.Content = nil
	item.CreatedAt = item.CreatedAt.UTC()
	b, _ := json.Marshal(item)
	return sha256.Sum256(b)
}

func validInboxItem(item notification.Item) bool {
	for _, text := range []string{item.Order, item.Source.Provider, item.Source.Channel, item.Source.Instance,
		item.Source.SubscriptionID, item.Source.ItemKey, item.Source.Project, item.Source.Object, item.Source.URL} {
		if len(text) > 4096 || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return false
		}
	}
	return validText(item.DeliveryID) && len(item.DeliveryID) <= 4096 &&
		validIntegrationSource(item.Source) && item.Key == item.Source.ItemKey &&
		!item.CreatedAt.IsZero() && validText(item.Text) && len(item.Text) <= maxTextBytes && item.Content.Valid()
}

func (e InboxEntry) validate(id MemberIdentity) error {
	if e.Version != 1 {
		return ErrVersion
	}
	if e.MemberIdentity != id {
		return ErrIdentity
	}
	if e.Seq == 0 || e.ReceivedAt.IsZero() || !validInboxItem(e.Item) {
		return ErrInvalid
	}
	return nil
}

// loadInbox runs before Spawn, under the Store lock. Only receipt hashes and
// sequence metadata survive recovery in memory; bodies are read one at a time.
func (s *Store) loadInbox(a *AgentInbox, requested InboxLimits) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, err := s.memberPath(a.id, "agent.json", true)
	if err != nil {
		return err
	}
	a.dir = filepath.Join(s.dir, "inboxes", a.id.MembershipID)
	var header inboxHeader
	err = readSnapshot(filepath.Join(a.dir, "inbox.json"), &header, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		// An orphaned entry directory is never reset to an empty inbox.
		if entries, e := os.ReadDir(a.dir); e == nil {
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), ".snapshot-") {
					return ErrInvalid
				}
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		limits := requested
		if limits == (InboxLimits{}) {
			limits = DefaultInboxLimits()
		}
		header = inboxHeader{Version: 1, MemberIdentity: a.id, Limits: limits}
		if err := s.save(filepath.Join(a.dir, "inbox.json"), header, maxPrivateFileBytes); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if header.Version != 1 {
		return ErrVersion
	}
	if header.MemberIdentity != a.id {
		return ErrIdentity
	}
	if !header.Limits.valid() {
		return ErrInvalid
	}
	if requested != (InboxLimits{}) && requested != header.Limits {
		return ErrConflict
	}
	a.limits = header.Limits
	dir, err := os.Open(a.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		files, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, file := range files {
			if file.Name() == "inbox.json" || file.Name() == "consumption.json" || strings.HasPrefix(file.Name(), ".snapshot-") {
				continue // Uncommitted temporary files are never authoritative.
			}
			var entry InboxEntry
			if err := readSnapshot(filepath.Join(a.dir, file.Name()), &entry, a.limits.EntryBytes); err != nil {
				return err
			}
			if err := entry.validate(a.id); err != nil {
				return err
			}
			if file.Name() != inboxEntryName(entry.Seq) {
				return ErrInvalid
			}
			if _, exists := a.receipts[entry.Item.DeliveryID]; exists {
				return ErrInvalid
			}
			size, err := inboxEntrySize(entry)
			if err != nil {
				return fmt.Errorf("%w: encode inbox entry: %w", ErrInvalid, err)
			}
			a.bytes += size
			a.latest = max(a.latest, entry.Seq)
			a.receipts[entry.Item.DeliveryID] = inboxIndex{entry.Seq, inboxDigest(entry.Item)}
			if len(a.receipts) > a.limits.Entries || a.bytes > a.limits.Bytes {
				return ErrCapacity
			}
		}
		if err == io.EOF {
			break
		}
	}
	// Unique, positive file sequences with max == count imply no gaps.
	if a.latest != uint64(len(a.receipts)) {
		return ErrInvalid
	}
	return nil
}

func (s *Store) saveInboxEntry(a *AgentInbox, entry InboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.memberPath(a.id, "agent.json", true); err != nil {
		return err
	}
	path := filepath.Join(a.dir, inboxEntryName(entry.Seq))
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return ErrConflict // Immutable entries can never be replaced.
	}
	return s.save(path, entry, a.limits.EntryBytes)
}

func (s *Store) previewInbox(a *AgentInbox, after, snapshot uint64, limit int) (InboxPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := InboxPage{MemberIdentity: a.id, SnapshotSeq: snapshot, NextAfter: after, Entries: []InboxEntry{}}
	if err := s.check(false); err != nil {
		return InboxPage{}, err
	}
	var bytes int64
	for seq := after + 1; seq <= snapshot && len(page.Entries) < limit; seq++ {
		var entry InboxEntry
		if err := readSnapshot(filepath.Join(a.dir, inboxEntryName(seq)), &entry, a.limits.EntryBytes); err != nil {
			return InboxPage{}, err
		}
		if err := entry.validate(a.id); err != nil {
			return InboxPage{}, err
		}
		if entry.Seq != seq {
			return InboxPage{}, ErrInvalid
		}
		size, err := inboxEntrySize(entry)
		if err != nil {
			return InboxPage{}, fmt.Errorf("%w: encode inbox entry: %w", ErrInvalid, err)
		}
		if bytes+size > a.limits.PageBytes {
			break
		}
		page.Entries = append(page.Entries, entry)
		bytes += size
		page.NextAfter = seq
	}
	page.HasMore = page.NextAfter < snapshot
	return page, nil
}
