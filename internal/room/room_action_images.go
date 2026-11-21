package room

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/go-aie/teamx/internal/app/platformtools"
	"github.com/go-aie/teamx/internal/platform/attachment"
)

// Only executeReferenceAction installs this value, after authenticating the
// running turn. It is independent of model read pages, receipts and budgets.
type roomActionContextKey struct{}
type roomActionContext struct {
	ServiceInfo    `json:"-"`
	MemberIdentity `json:"-"`
	Turn           uint64
	ID             string
	Input          string // immutable original JSON, for per-call strategy selection only
}

type roomActionImageRef = platformtools.ImageRef
type roomActionImage struct {
	Ref        roomActionImageRef
	Attachment RoomAttachment
	SHA256     string
	Data       []byte
}

// One native publication pin and all object pins live until the action session
// closes. No Store, attachment lifecycle or service mutation lock spans upload.
type roomActionImages struct {
	mu        sync.RWMutex
	closed    bool
	service   *service
	call      roomActionContext
	execution turnExecution
	images    []roomActionImage
	guard     func(context.Context) error
	release   func()
}

// The action owner already holds service.changes.RLock. In particular, never
// reacquire that lock here: a pending writer would deadlock recursive readers.
func (s *service) prepareRoomActionImages(ctx context.Context, refs []roomActionImageRef, guard func(context.Context) error) (_ *roomActionImages, err error) {
	call, ok := ctx.Value(roomActionContextKey{}).(roomActionContext)
	if !ok || call.ServiceInfo != s.info || call.MemberIdentity.RoomIdentity != s.info.RoomIdentity || call.Turn == 0 || !validText(call.ID) || guard == nil {
		return nil, ErrIdentity
	}
	if len(refs) == 0 || len(refs) > roomAttachmentCount {
		return nil, ErrRequest
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.MessageSeq == 0 || !validRoomAttachmentID(ref.AttachmentID) || seen[ref.AttachmentID] {
			return nil, ErrRequest
		}
		seen[ref.AttachmentID] = true
	}
	if err := guard(ctx); err != nil {
		return nil, err
	}
	execution, err := s.executionBindingForTurn(call.MemberIdentity, call.Turn)
	if err != nil {
		return nil, err
	}
	store := s.store
	room, cache, release, err := store.pinRoomPublication(call.MemberIdentity, nil, false)
	if err != nil {
		return nil, err
	}
	lease := &roomActionImages{service: s, call: call, execution: execution, guard: guard, release: release}
	defer func() {
		if err != nil {
			lease.Close()
		}
	}()
	room, err = store.readPinnedRoom(room, cache)
	if err != nil {
		return nil, err
	}
	authors := make([]string, len(refs))
	for i, ref := range refs {
		if ref.MessageSeq <= room.ClearedThrough || ref.MessageSeq > room.LatestSeq {
			return nil, ErrReadExpired
		}
		msg := room.Messages[ref.MessageSeq-room.ClearedThrough-1]
		if msg.Seq != ref.MessageSeq || msg.Author.Kind != "human" || msg.Event != nil || msg.Source != nil || !roomVisible(msg, agentCallerIdentity(call.MemberIdentity), true) || !roomDeliverable(msg, call.MemberIdentity) {
			return nil, ErrIdentity
		}
		n := slices.IndexFunc(msg.Attachments, func(a RoomAttachment) bool { return a.ID == ref.AttachmentID })
		if n < 0 {
			return nil, ErrIdentity
		}
		lease.images = append(lease.images, roomActionImage{Ref: ref, Attachment: msg.Attachments[n]})
		authors[i] = msg.Author.ID
	}
	// Metadata and object pin acquisition follow attachmentLife -> Store.mu.
	// Hold the lifecycle read lock only for local files, never a remote call.
	err = func() error {
		store.attachmentLife.RLock()
		defer store.attachmentLife.RUnlock()
		metas := make([]roomAttachmentMeta, len(refs))
		pinned := []string{}
		store.mu.Lock()
		pinErr := store.check(false)
		var total int64
		for i, img := range lease.images {
			if pinErr != nil {
				break
			}
			var meta roomAttachmentMeta
			meta, pinErr = store.attachmentMeta(img.Ref.AttachmentID)
			if pinErr != nil {
				break
			}
			a := store.attachmentStorage()
			if meta.Attachment != img.Attachment || meta.Operator != authors[i] || a.active[img.Attachment.ID] || !a.references[img.Attachment.ID] || !store.attachmentAvailable(meta, a.references) {
				pinErr = ErrIdentity
				break
			}
			total += meta.Attachment.Size
			if total > roomAttachmentTotalBytes {
				pinErr = attachment.ErrLimit
				break
			}
			metas[i] = meta
			a.pins[img.Attachment.ID]++
			pinned = append(pinned, img.Attachment.ID)
		}
		// Even partially prepared sets must release every pin on failure.
		lease.release = sync.OnceFunc(func() {
			store.mu.Lock()
			for _, id := range pinned {
				store.attachmentStorage().pins[id]--
				if store.attachmentStorage().pins[id] == 0 {
					delete(store.attachmentStorage().pins, id)
				}
			}
			store.mu.Unlock()
			release()
		})
		store.mu.Unlock()
		if pinErr != nil {
			return pinErr
		}
		for i, meta := range metas {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, _, err := store.readAttachment(meta)
			if err != nil {
				return err
			}
			mime, err := attachment.ValidateImage(data, attachment.ImageLimits{MaxBytes: roomAttachmentBytes, MaxPixels: roomAttachmentPixels})
			if err != nil {
				return err
			}
			config, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || mime != meta.Attachment.MIMEType || config.Width != meta.Attachment.Width || config.Height != meta.Attachment.Height {
				return attachment.ErrFormat
			}
			lease.images[i].Data, lease.images[i].SHA256 = data, meta.SHA256
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	if err = lease.Check(ctx); err != nil {
		return nil, err
	}
	return lease, nil
}

func (l *roomActionImages) Check(ctx context.Context) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return ErrClosing
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.guard(ctx); err != nil {
		return err
	}
	s := l.service
	if s.closing.Load() {
		return ErrClosing
	}
	if s.restarting.Load() {
		return ErrBusy
	}
	if l.call.ServiceInfo != s.info {
		return ErrIdentity
	}
	// A captured identity chooses these private paths. Never use memberPath or
	// runningTurnLocked here: both materialize the entire Room log.
	store := s.store
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.check(false); err != nil {
		return err
	}
	id := l.call.MemberIdentity
	if id.RoomID != store.roomID || id.RoomInstance != store.instance {
		return ErrIdentity
	}
	dir := filepath.Join(store.dir, "agents", id.MembershipID)
	for _, path := range []string{filepath.Join(store.dir, "agents"), dir} {
		if err := noSymlinkDir(path); err != nil {
			return err
		}
	}
	data, _, err := readAttachmentFile(dir, "agent.json", maxPrivateFileBytes)
	if err != nil {
		return err
	}
	var agent AgentSnapshot
	if err := decodeRequestJSON(string(data), &agent); err != nil {
		return err
	}
	if agent.validate() != nil || agent.MemberIdentity != id || agent.TurnNumber != l.execution.Turn || agent.Turn == nil || agent.Turn.Number != l.execution.Turn || agent.Turn.Status != "running" {
		return ErrIdentity
	}
	// Check file identity as well as the adapter's full reference validation.
	// The small second read reuses its generation/pending-directory rules.
	data, _, err = readAttachmentFile(dir, "session.json", maxPrivateFileBytes)
	var captured sessionReference
	if err == nil {
		if err := decodeRequestJSON(string(data), &captured); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ref, exists, err := readSessionReference(filepath.Join(dir, "session.json"), id)
	if err != nil {
		return err
	}
	if ref != captured || ref.Pending {
		return ErrIdentity
	}
	generation := "local_" + id.MembershipID
	if exists {
		generation = ref.Directory
	}
	if generation != l.execution.Generation {
		return ErrIdentity
	}
	return ctx.Err()
}

func (l *roomActionImages) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.release()
	for i := range l.images {
		l.images[i].Data = nil
	}
}

// Bytes projects only the material required by platform adapters. Pin ownership
// and execution checks remain private to the Room publication owner.
func (l *roomActionImages) Bytes() []platformtools.Image {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return nil
	}
	result := make([]platformtools.Image, len(l.images))
	for i, img := range l.images {
		result[i] = platformtools.Image{Data: img.Data, MIME: img.Attachment.MIMEType, SHA256: img.SHA256}
	}
	return result
}

func (s *service) prepareRoomActionImageLease(ctx context.Context, refs []platformtools.ImageRef, guard func(context.Context) error) (platformtools.ImageLease, error) {
	lease, err := s.prepareRoomActionImages(ctx, refs, guard)
	if err != nil {
		return nil, err
	}
	return lease, nil
}
