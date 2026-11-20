package room

import (
	"context"
	"slices"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/attachment"
)

// Internal selection evidence, shared by preparation, replay and confirmation.
// It is never a tool argument or an authority to read another message's image.
type roomAttachmentPage struct {
	session *inputSession
	attempt inputstream.ReadAttempt
}

// Compose sources at the existing material boundary. Platform account gates
// receive only platform notices/accesses; single-Room services need no manager.
func (s *service) roomMaterialAttachments(remote *materialAttachments) *materialAttachments {
	return &materialAttachments{
		download: func(ctx context.Context, req attachmentRequest, reader *attachmentReader, limits attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
			if req.Local {
				return s.downloadRoomAttachment(ctx, req, reader, limits)
			}
			if remote == nil || remote.download == nil {
				return nil, "", nil, attachment.ErrUnavailable
			}
			return remote.download(ctx, req, reader, limits)
		},
		check: func(ctx context.Context, info ServiceInfo, reader attachmentReader, turn TurnRecord, notices []materialNotice, accesses []*attachmentAccess) error {
			var platformNotices []materialNotice
			var platformAccesses []*attachmentAccess
			for _, n := range notices {
				if !n.request.Local {
					platformNotices = append(platformNotices, n)
					continue
				}
				if n.request.ServiceInfo != info {
					return ErrIdentity
				}
				s.store.mu.Lock()
				message, err := s.roomAttachmentMessageLocked(ctx, n.request, &reader)
				if err == nil && (!slices.Equal(message.Attachments, n.attachments) || message.Text != n.text || message.Author.ID != n.author) {
					err = ErrIdentity
				}
				s.store.mu.Unlock()
				if err != nil {
					return err
				}
			}
			for _, access := range accesses {
				if access == nil {
					return ErrIdentity
				}
				if access.local == nil {
					platformAccesses = append(platformAccesses, access)
					continue
				}
				if access.request.ServiceInfo != info || access.reader == nil || *access.reader != reader {
					return ErrIdentity
				}
				s.store.mu.Lock()
				message, err := s.roomAttachmentMessageLocked(ctx, access.request, &reader)
				if err == nil && !slices.Contains(message.Attachments, access.local.Attachment) {
					err = ErrIdentity
				}
				s.store.mu.Unlock()
				if err != nil {
					return err
				}
			}
			if remote != nil && remote.check != nil && (len(platformNotices) > 0 || len(platformAccesses) > 0) {
				return remote.check(ctx, info, reader, turn, platformNotices, platformAccesses)
			}
			return ctx.Err()
		},
		checkInbox: func(ctx context.Context, info ServiceInfo, reader attachmentReader, notices []materialNotice) error {
			if remote != nil && remote.checkInbox != nil {
				return remote.checkInbox(ctx, info, reader, slices.DeleteFunc(slices.Clone(notices), func(n materialNotice) bool { return n.request.Local }))
			}
			return ctx.Err()
		},
	}
}

// Caller holds Store.mu. Validate the actual member/execution and exact selected
// range before looking up bytes. Message descriptors are native publication data,
// including materialized InputLog messages, never the history metadata index.
func (s *service) roomAttachmentMessageLocked(ctx context.Context, req attachmentRequest, reader *attachmentReader) (RoomMessage, error) {
	if err := ctx.Err(); err != nil {
		return RoomMessage{}, err
	}
	if s.closing.Load() || s.restarting.Load() || !req.Local || req.ServiceInfo != s.info || reader == nil || reader.RoomIdentity != s.info.RoomIdentity || reader.TurnNumber == 0 || req.AgentID != "" || req.MembershipID != "" || req.Partition != "" || req.SourceRef != "" {
		return RoomMessage{}, ErrIdentity
	}
	var session *inputSession
	if req.Range != nil {
		if req.Page != nil || req.Range.stream.provider != "room" || req.Range.checkLocked(req, reader) != nil {
			return RoomMessage{}, ErrIdentity
		}
		session = req.Range.session
	} else if req.Page != nil {
		session = req.Page.session
	} else {
		return RoomMessage{}, ErrIdentity
	}
	if session == nil || session.service != s || session.storage.store != s.store || session.storage.id != reader.MemberIdentity || session.execution.Turn != reader.TurnNumber || session.storage.generation != session.execution.Generation {
		return RoomMessage{}, ErrIdentity
	}
	if session.executionContext != nil && session.executionContext.Err() != nil {
		return RoomMessage{}, ErrClosing
	}
	execution, err := session.storage.executionLocked(reader.TurnNumber)
	if err != nil || execution != session.execution {
		return RoomMessage{}, ErrIdentity
	}
	state, err := session.storage.repository.State(execution)
	if err != nil {
		return RoomMessage{}, err
	}
	if state.Closed {
		return RoomMessage{}, ErrIdentity
	}
	location := inputstream.Location{Partition: roomPartitionRef(s.info.RoomIdentity), Seq: req.Seq}
	if req.Page != nil {
		selected := req.Page.attempt
		found := false
		for _, a := range state.Attempts {
			if a.ID == selected.ID && a.Kind == "room" && a.Phase != "failed_before_output" && a.Snapshot == selected.Snapshot && a.From == selected.From && a.Through == selected.Through && slices.Equal(a.Entries, selected.Entries) && slices.Contains(a.Entries, location) {
				found = true
				break
			}
		}
		if !found {
			return RoomMessage{}, ErrIdentity
		}
	}
	room, err := s.store.loadRoom()
	if err != nil {
		return RoomMessage{}, err
	}
	if req.Seq <= room.ClearedThrough || req.Seq > room.LatestSeq {
		return RoomMessage{}, ErrReadExpired
	}
	message := room.Messages[req.Seq-room.ClearedThrough-1]
	if message.Author.Kind != "human" || message.Event != nil || !roomVisible(message, agentCallerIdentity(reader.MemberIdentity), req.Range != nil) || !roomDeliverable(message, reader.MemberIdentity) {
		return RoomMessage{}, ErrIdentity
	}
	if req.Range != nil {
		bound := req.Range.stream.bound
		if req.Range.location != location || bound.Partition != location.Partition || bound.Reclaimed != room.ClearedThrough || bound.Through > room.LatestSeq || req.Seq > bound.Through || !slices.Equal(message.Attachments, req.Range.attachments) {
			return RoomMessage{}, ErrReadExpired
		}
	}
	return message, nil
}

func (s *service) downloadRoomAttachment(ctx context.Context, req attachmentRequest, reader *attachmentReader, limits attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
	// Same lifecycle order as HTTP reads. File I/O stays outside Store.mu;
	// native page/history pins retain the message and object pins retain bytes.
	s.changes.RLock()
	defer s.changes.RUnlock()
	s.store.attachmentLife.RLock()
	defer s.store.attachmentLife.RUnlock()
	if !validRoomAttachmentID(req.Path) || limits.MaxBytes <= 0 || limits.MaxBytes > attachment.MaxBytes || limits.MaxPixels <= 0 {
		return nil, "", nil, ErrRequest
	}
	s.store.mu.Lock()
	message, err := s.roomAttachmentMessageLocked(ctx, req, reader)
	var meta roomAttachmentMeta
	if err == nil && !slices.ContainsFunc(message.Attachments, func(a RoomAttachment) bool { return a.ID == req.Path }) {
		err = ErrIdentity
	}
	if err == nil {
		meta, err = s.store.attachmentMeta(req.Path)
		if err == nil && (meta.Operator != message.Author.ID || !slices.Contains(message.Attachments, meta.Attachment) || s.store.attachmentStorage().active[req.Path] || !s.store.attachmentAvailable(meta, s.store.attachmentStorage().references)) {
			err = ErrIdentity
		}
	}
	if err != nil {
		s.store.mu.Unlock()
		return nil, "", nil, err
	}
	s.store.attachmentStorage().pins[req.Path]++
	s.store.mu.Unlock()
	defer func() { s.store.mu.Lock(); s.store.attachmentStorage().pins[req.Path]--; s.store.mu.Unlock() }()
	if meta.Attachment.Size > limits.MaxBytes || int64(meta.Attachment.Width) > limits.MaxPixels/int64(meta.Attachment.Height) {
		return nil, "", nil, attachment.ErrLimit
	}
	data, _, err := s.store.readAttachment(meta)
	if err != nil {
		return nil, "", nil, attachment.ErrUnavailable
	}
	mime, err := attachment.ValidateImage(data, limits)
	if err != nil {
		return nil, "", nil, err
	}
	s.store.mu.Lock()
	current, err := s.roomAttachmentMessageLocked(ctx, req, reader)
	if err == nil {
		var currentMeta roomAttachmentMeta
		currentMeta, err = s.store.attachmentMeta(req.Path)
		if err == nil && currentMeta != meta {
			err = ErrIdentity
		}
	}
	if err == nil && (!slices.Contains(current.Attachments, meta.Attachment) || mime != meta.Attachment.MIMEType) {
		err = ErrIdentity
	}
	s.store.mu.Unlock()
	if err != nil {
		return nil, "", nil, err
	}
	copy := *reader
	return data, mime, &attachmentAccess{request: req, reader: &copy, local: &meta}, nil
}
