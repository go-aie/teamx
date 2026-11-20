package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform/attachment"
)

// Lock order: attachmentLife -> roomCommit (publications only) -> Store.mu -> writer/path locks. No attachment
// protection is held over an Actor call. Service.changes encloses HTTP work.
// File receive/decode/read runs outside mu; pins prevent reclamation until the
// actual operation settles, including after the requesting client disconnects.
type roomAttachmentStorage struct {
	recoveryUncertain bool
	confirmed         map[string]bool
	// Rebuildable index of the last validated native root, never a publication
	// authority. In particular it includes materialized InputLog messages.
	latest         uint64
	committed      map[uint64][]RoomAttachment
	references     map[string]bool
	clearedThrough uint64
	reserved       map[string]int64
	active         map[string]bool
	pins           map[string]int
	unconfirmed    map[string]bool
	limits         roomAttachmentLimits
	now            func() time.Time
	quotaBytes     int64
	quotaObjects   int
}

var roomAttachmentDecoders = make(chan struct{}, 2)

func (s *Store) attachmentStorage() *roomAttachmentStorage {
	if s.attachments == nil {
		s.attachments = &roomAttachmentStorage{active: map[string]bool{}, confirmed: map[string]bool{}, reserved: map[string]int64{}, pins: map[string]int{}, unconfirmed: map[string]bool{}, limits: attachmentLimits(imageMaterialConfig{}), now: time.Now, quotaBytes: roomAttachmentQuota, quotaObjects: roomAttachmentObjects}
	}
	return s.attachments
}
func (s *Store) attachmentDir() string { return filepath.Join(s.dir, "attachments") }
func (s *Store) attachmentPath(id string) string {
	return filepath.Join(s.attachmentDir(), "objects", id)
}
func noSymlinkDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return attachmentError("unsafe attachment directory")
	}
	return nil
}
func (s *Store) checkAttachmentDirs() error {
	for _, path := range []string{s.attachmentDir(), filepath.Join(s.attachmentDir(), "objects"), filepath.Join(s.attachmentDir(), "tmp")} {
		if err := noSymlinkDir(path); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) ensureAttachmentDirs() error {
	for _, path := range []string{s.attachmentDir(), filepath.Join(s.attachmentDir(), "objects"), filepath.Join(s.attachmentDir(), "tmp")} {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := noSymlinkDir(path); err != nil {
			return err
		}
		if err := s.ops.syncDir(filepath.Dir(path)); err != nil {
			return err
		}
	}
	return nil
}

// Lstat plus SameFile rejects links and replaced files. OpenRoot also confines
// resolution to the Room object even if a local path is changed concurrently.
func readAttachmentFile(dir, name string, limit int64) ([]byte, os.FileInfo, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, nil, attachmentError("unsafe or oversized attachment file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, nil, attachmentError("attachment changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) != info.Size() {
		return nil, nil, attachmentError("attachment size mismatch")
	}
	return b, info, nil
}
func (s *Store) attachmentMeta(id string) (roomAttachmentMeta, error) {
	var m roomAttachmentMeta
	if !validRoomAttachmentID(id) {
		return m, ErrRequest
	}
	if err := s.checkAttachmentDirs(); err != nil {
		return m, err
	}
	dir := s.attachmentPath(id)
	if err := noSymlinkDir(dir); err != nil {
		return m, err
	}
	b, _, err := readAttachmentFile(dir, "meta.json", 4096)
	if err != nil {
		return m, err
	}
	if err := decodeRequestJSON(string(b), &m); err != nil {
		return m, err
	}
	digest, err := hex.DecodeString(m.SHA256)
	if err != nil || len(digest) != 32 || m.RoomIdentity != (RoomIdentity{RoomID: s.roomID, RoomInstance: s.instance}) || !validText(m.Operator) || m.CreatedAt.IsZero() || m.Attachment.ID != id || !validRoomAttachments(RoomMessage{Author: Author{"human", m.Operator}, Attachments: []RoomAttachment{m.Attachment}}) {
		return m, ErrInvalid
	}
	return m, nil
}
func (s *Store) readAttachment(m roomAttachmentMeta) ([]byte, os.FileInfo, error) {
	b, info, err := readAttachmentFile(s.attachmentPath(m.Attachment.ID), "data", roomAttachmentBytes)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(b)
	if int64(len(b)) != m.Attachment.Size || hex.EncodeToString(digest[:]) != m.SHA256 {
		return nil, nil, attachmentError("attachment integrity check failed")
	}
	return b, info, nil
}
func attachmentReferences(v RoomSnapshot) map[string]bool {
	refs := map[string]bool{}
	for _, m := range v.Messages {
		for _, a := range m.Attachments {
			refs[a.ID] = true
		}
	}
	return refs
}
func (s *Store) attachmentAvailable(m roomAttachmentMeta, refs map[string]bool) bool {
	a := s.attachmentStorage()
	return (!a.recoveryUncertain || a.confirmed[m.Attachment.ID]) && !a.unconfirmed[m.Attachment.ID] && (refs[m.Attachment.ID] || a.now().Before(m.CreatedAt.Add(roomAttachmentTTL)))
}

// collectAttachments is called only while mu is held. A failed/uncertain root
// publication disables collection through check(true); the published root is
// the authority, including materialized InputLog records.
func (s *Store) collectAttachments() error {
	if err := s.check(true); err != nil {
		return err
	}
	if s.attachmentStorage().recoveryUncertain {
		return nil
	}
	v, err := s.loadRoom()
	if err != nil {
		return err
	}
	a := s.attachmentStorage()
	refs := attachmentReferences(v)
	if _, err := os.Lstat(s.attachmentDir()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := s.checkAttachmentDirs(); err != nil {
		return err
	}
	removed := 0
	for _, kind := range []string{"objects", "tmp"} {
		dir := filepath.Join(s.attachmentDir(), kind)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if removed >= 64 {
				break
			}
			if !validRoomAttachmentID(e.Name()) || a.active[e.Name()] || a.pins[e.Name()] > 0 || refs[e.Name()] || e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			info, err := e.Info()
			if err != nil {
				continue
			}
			expired := !a.now().Before(info.ModTime().Add(roomAttachmentTTL))
			if kind == "objects" {
				m, err := s.attachmentMeta(e.Name())
				if err != nil {
					continue
				}
				expired = !a.now().Before(m.CreatedAt.Add(roomAttachmentTTL))
			}
			if expired && !a.unconfirmed[e.Name()] {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				removed++
			}
		}
		if removed > 0 {
			if err := s.ops.syncDir(dir); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) attachmentUsage(replacingTemp string) (int64, int, error) {
	var size int64
	count := 0
	for _, kind := range []string{"objects", "tmp"} {
		dir := filepath.Join(s.attachmentDir(), kind)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return 0, 0, err
		}
		for _, entry := range entries {
			if kind == "tmp" && (entry.Name() == replacingTemp || s.attachmentStorage().reserved[entry.Name()] > 0) {
				continue
			}
			count++
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				return 0, 0, attachmentError("unsafe attachment object")
			}
			files, err := os.ReadDir(filepath.Join(dir, entry.Name()))
			if err != nil {
				return 0, 0, err
			}
			for _, f := range files {
				info, err := f.Info()
				if err != nil {
					return 0, 0, err
				}
				if !info.Mode().IsRegular() {
					return 0, 0, attachmentError("unsafe attachment file")
				}
				size += info.Size()
			}
		}
	}
	return size, count, nil
}
func writeAttachmentFile(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
func (s *Store) uploadAttachment(ctx context.Context, operator, id, name string, reader io.Reader) (roomAttachmentMeta, error) {
	s.attachmentLife.RLock()
	defer s.attachmentLife.RUnlock()
	if !validRoomAttachmentID(id) || !validText(operator) {
		return roomAttachmentMeta{}, ErrRequest
	}
	s.mu.Lock()
	a := s.attachmentStorage()
	limits := a.limits
	err := s.check(true)
	var v RoomSnapshot
	if err == nil {
		v, err = s.loadRoom()
	}
	if err == nil {
		err = s.ensureAttachmentDirs()
	}
	if err == nil && (a.active[id] || a.pins[id] > 0) {
		err = ErrBusy
	}
	existing := false
	if err == nil {
		_, statErr := os.Lstat(s.attachmentPath(id))
		existing = statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			err = statErr
		}
	}
	if existing {
		// Reconfirming an immutable object is not a new upload. Lowered
		// execution limits must not change its original upload receipt.
		limits.ImageBytes = roomAttachmentBytes
		limits.MaxPixels = roomAttachmentPixels
	}
	if err == nil && !existing {
		err = s.collectAttachments()
		if err == nil {
			var size int64
			var count int
			size, count, err = s.attachmentUsage(id)
			for _, reservation := range a.reserved {
				size += reservation
				count++
			}

			if err == nil && (count >= a.quotaObjects || size+limits.ImageBytes+4096 > a.quotaBytes) {
				err = fmt.Errorf("%w: Room attachment storage is full", ErrCapacity)
			}
		}
	}
	if err == nil {
		a.active[id] = true
		if !existing {
			a.reserved[id] = limits.ImageBytes + 4096
		}
	}
	s.mu.Unlock()
	if err != nil {
		return roomAttachmentMeta{}, err
	}
	defer func() { s.mu.Lock(); delete(a.active, id); delete(a.reserved, id); s.mu.Unlock() }()
	name = cleanAttachmentName(name)
	// Receive into a private temporary object. Same-ID retries are serialized.
	tmp := filepath.Join(s.attachmentDir(), "tmp", id)
	if !existing {
		if err := os.Mkdir(tmp, 0700); errors.Is(err, os.ErrExist) {
			if err = noSymlinkDir(tmp); err != nil {
				return roomAttachmentMeta{}, err
			}
			if err = os.RemoveAll(tmp); err != nil {
				return roomAttachmentMeta{}, err
			}
			if err = os.Mkdir(tmp, 0700); err != nil {
				return roomAttachmentMeta{}, err
			}
		} else if err != nil {
			return roomAttachmentMeta{}, err
		}
		defer os.RemoveAll(tmp)
	}
	var data []byte
	if existing {
		data, err = io.ReadAll(io.LimitReader(reader, limits.ImageBytes+1))
	} else {
		var file *os.File
		file, err = os.OpenFile(filepath.Join(tmp, "data"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			var received int64
			received, err = io.Copy(file, io.LimitReader(reader, limits.ImageBytes+1))
			if err == nil && received > limits.ImageBytes {
				err = attachmentError("image exceeds upload byte limit")
			}
			if err == nil {
				err = file.Sync()
			}
			err = errors.Join(err, file.Close())
		}
		if err == nil {
			data, _, err = readAttachmentFile(tmp, "data", limits.ImageBytes)
		}
	}
	if err != nil {
		return roomAttachmentMeta{}, err
	}
	if ctx.Err() != nil {
		return roomAttachmentMeta{}, ctx.Err()
	}
	select {
	case roomAttachmentDecoders <- struct{}{}:
		defer func() { <-roomAttachmentDecoders }()
	case <-ctx.Done():
		return roomAttachmentMeta{}, ctx.Err()
	}
	mime, err := attachment.ValidateImage(data, attachment.ImageLimits{MaxBytes: limits.ImageBytes, MaxPixels: limits.MaxPixels})
	if err != nil {
		return roomAttachmentMeta{}, attachmentError("image exceeds limits or has invalid format")
	}
	if !slices.Contains(limits.MIMETypes, mime) {
		return roomAttachmentMeta{}, attachmentError("choose a PNG, JPEG or WebP image")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return roomAttachmentMeta{}, ErrRequest
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return roomAttachmentMeta{}, attachmentError("image is truncated or cannot be decoded")
	}
	digest := sha256.Sum256(data)
	m := roomAttachmentMeta{RoomIdentity: v.RoomIdentity, Operator: operator, CreatedAt: a.now().UTC(), SHA256: hex.EncodeToString(digest[:]), Attachment: RoomAttachment{id, "image", name, mime, int64(len(data)), config.Width, config.Height}}
	if existing {
		old, err := s.attachmentMeta(id)
		if err != nil {
			return m, err
		}
		if old.Operator != operator || old.SHA256 != m.SHA256 || old.Attachment.Name != name {
			return m, ErrConflict
		}
		if _, _, err := s.readAttachment(old); err != nil {
			return m, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		current, err := s.loadRoom()
		if err != nil {
			return m, err
		}
		if !attachmentReferences(current)[id] && !a.now().Before(old.CreatedAt.Add(roomAttachmentTTL)) {
			return m, attachmentError("attachment expired")
		}
		if err := s.ops.syncDir(s.attachmentPath(id)); err != nil {
			return m, err
		}
		if err := s.ops.syncDir(filepath.Dir(s.attachmentPath(id))); err != nil {
			return m, err
		}
		delete(a.unconfirmed, id)
		a.confirmed[id] = true
		return old, nil
	}
	meta, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	if err = writeAttachmentFile(filepath.Join(tmp, "meta.json"), meta); err != nil {
		return m, err
	}
	if err = s.ops.syncDir(tmp); err != nil {
		return m, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(true); err != nil {
		return m, err
	}
	a.unconfirmed[id] = true
	if err = s.ops.rename(tmp, s.attachmentPath(id)); err != nil {
		return m, err
	}
	if err = s.ops.syncDir(filepath.Dir(s.attachmentPath(id))); err != nil {
		return m, err
	}
	delete(a.unconfirmed, id)
	a.confirmed[id] = true
	return m, nil
}

// pinAttachment fixes the native association before reading outside Store.mu.
// The release function belongs to the operation, not its request context.
func (s *Store) pinAttachment(operator, id string, seq uint64) (roomAttachmentMeta, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(false); err != nil {
		return roomAttachmentMeta{}, nil, err
	}
	v, err := s.loadRoom()
	if err != nil {
		return roomAttachmentMeta{}, nil, err
	}
	m, err := s.attachmentMeta(id)
	if err != nil {
		return m, nil, attachmentError("image is missing or unavailable")
	}
	a := s.attachmentStorage()
	if a.active[id] || !s.attachmentAvailable(m, attachmentReferences(v)) {
		return m, nil, attachmentError("attachment expired or unavailable")
	}
	if seq == 0 {
		if operator != m.Operator {
			return m, nil, ErrIdentity
		}
	} else {
		if seq <= v.ClearedThrough || seq > v.LatestSeq {
			return m, nil, ErrIdentity
		}
		message := v.Messages[seq-v.ClearedThrough-1]
		if !roomVisible(message, Caller{Author: Author{"human", operator}}, true) || !slices.ContainsFunc(message.Attachments, func(a RoomAttachment) bool { return a == m.Attachment }) {
			return m, nil, ErrIdentity
		}
	}
	a.pins[id]++
	var once sync.Once
	release := func() { once.Do(func() { s.mu.Lock(); a.pins[id]--; s.mu.Unlock() }) }
	return m, release, nil
}

// The Room commit task releases these pins only after SaveRoom settles. No
// mutex or lifecycle read-lock is carried across the next Store operation.
func (s *Store) prepareRoomAttachments(operator string, inputs []RoomAttachmentInput) (out []RoomAttachment, release func(), err error) {
	if !validAttachmentInputs(inputs) {
		return nil, nil, ErrRequest
	}
	s.attachmentLife.RLock()
	defer s.attachmentLife.RUnlock()
	releases := []func(){}
	release = func() {
		for _, done := range releases {
			done()
		}
	}
	defer func() {
		if err != nil {
			release()
		}
	}()
	var total int64
	for _, in := range inputs {
		var m roomAttachmentMeta
		var done func()
		m, done, err = s.pinAttachment(operator, in.ID, 0)
		if err != nil {
			return nil, release, err
		}
		releases = append(releases, done)
		if _, _, err = s.readAttachment(m); err != nil {
			return nil, release, err
		}
		out = append(out, m.Attachment)
		total += m.Attachment.Size
	}
	s.mu.Lock()
	limits := s.attachmentStorage().limits
	s.mu.Unlock()
	if len(out) > limits.MaxImages || total > limits.TotalBytes {
		return nil, release, attachmentError("message images exceed limits")
	}
	for _, a := range out {
		if a.Size > limits.ImageBytes || int64(a.Width) > limits.MaxPixels/int64(a.Height) {
			return nil, release, attachmentError("image exceeds current limits")
		}
	}
	return out, release, nil
}

// Called with mu held and returns with mu held. Pins protect the objects while
// bytes are verified without the common lock. Publication then rechecks the
// metadata and inode before writing the root in this same serial boundary.
func (s *Store) verifyNewAttachmentsLocked(v RoomSnapshot) error {
	type check struct {
		meta roomAttachmentMeta
		info os.FileInfo
	}
	checks := []check{}
	a := s.attachmentStorage()
	refs := a.references
	defer func() {
		for _, c := range checks {
			a.pins[c.meta.Attachment.ID]--
		}
	}()
	for _, message := range v.Messages {
		if message.Seq <= a.latest {
			if !slices.Equal(message.Attachments, a.committed[message.Seq]) {
				return ErrConflict
			}
			continue
		}
		for _, desc := range message.Attachments {
			m, err := s.attachmentMeta(desc.ID)
			if err != nil {
				return err
			}
			if a.active[desc.ID] || m.RoomIdentity != v.RoomIdentity || m.Operator != message.Author.ID || m.Attachment != desc || (!s.attachmentAvailable(m, refs) && a.pins[desc.ID] == 0) {
				return attachmentError("attachment identity, descriptor or availability mismatch")
			}
			a.pins[desc.ID]++
			checks = append(checks, check{meta: m})
		}
	}
	if len(checks) == 0 {
		return nil
	}
	s.mu.Unlock()
	var readErr error
	for i := range checks {
		_, checks[i].info, readErr = s.readAttachment(checks[i].meta)
		if readErr != nil {
			break
		}
	}
	s.mu.Lock()
	if readErr != nil {
		return readErr
	}
	if err := s.check(true); err != nil {
		return err
	}
	for _, c := range checks {
		m, err := s.attachmentMeta(c.meta.Attachment.ID)
		if err != nil || m != c.meta {
			return attachmentError("attachment metadata changed before publication")
		}
		info, err := os.Lstat(filepath.Join(s.attachmentPath(m.Attachment.ID), "data"))
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, c.info) || info.Size() != c.info.Size() || info.ModTime() != c.info.ModTime() {
			return attachmentError("attachment changed before publication")
		}
	}
	return nil
}

// Caller holds mu, or is recovering the Store before publication.
func (s *Store) indexRoomAttachments(v RoomSnapshot) {
	a := s.attachmentStorage()
	if a.committed != nil && a.latest == v.LatestSeq && a.clearedThrough == v.ClearedThrough {
		return
	}
	a.latest = v.LatestSeq
	a.clearedThrough = v.ClearedThrough
	a.committed = map[uint64][]RoomAttachment{}
	a.references = map[string]bool{}
	for _, m := range v.Messages {
		if len(m.Attachments) > 0 {
			a.committed[m.Seq] = slices.Clone(m.Attachments)
			for _, desc := range m.Attachments {
				a.references[desc.ID] = true
			}
		}
	}
}
