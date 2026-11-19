package room

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/mark3labs/mcp-go/mcp"
)

// Transient results and their existing material access evidence. Never persisted
// as formal pages or carried between turns.
type historyPage struct {
	body       json.RawMessage
	imageBytes int64
	images     []mcp.ImageContent
	notices    []materialNotice
	accesses   []*attachmentAccess
}

type historyImage struct {
	Position int    `json:"position,omitempty"`
	Index    *int   `json:"content_index,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func (p *historyPage) result() *mcp.CallToolResult {
	out := mcp.NewToolResultText(string(p.body))
	for _, image := range p.images {
		out.Content = append(out.Content, image)
	}
	return out
}

// A server-created range binds the existing downloader to the exact retained
// record selected by History, instead of an Inbox formal page. It is never an
// Agent argument, a durable authorization or an alternative account authority.
type attachmentRange struct {
	session   *inputSession
	stream    historyStream
	location  inputstream.Location
	sourceRef platform.SourceRef
	text      string
}

func (a *attachmentRange) checkLocked(req attachmentRequest, reader *attachmentReader) error {
	s := a.session
	if s == nil || reader == nil || reader.MemberIdentity != s.storage.id || reader.TurnNumber != s.execution.Turn || req.ServiceInfo != s.service.info || req.Seq != a.location.Seq || s.storage.generation != s.execution.Generation || !slices.Contains(a.stream.locations, a.location) {
		return ErrIdentity
	}
	if s.executionContext != nil && s.executionContext.Err() != nil {
		return ErrClosing
	}
	if a.stream.provider == "room" {
		if req.AgentID != "" || req.MembershipID != "" || req.Partition != "" || req.SourceRef != "" {
			return ErrIdentity
		}
	} else if req.AgentID != reader.AgentID || req.MembershipID != reader.MembershipID || req.Partition != a.location.Partition || req.SourceRef != "" {
		return ErrIdentity
	}
	return nil
}

func (a *attachmentRange) source(ctx context.Context) (platform.SourceRef, string, error) {
	// The selected saved record is immutable. Revalidate its retention and
	// visibility using shared metadata/pins, without re-decoding bodies under the
	// manager's account gate. The original notice supplies the saved source/text.
	unpin, err := a.session.pinHistory(ctx, []historyStream{a.stream})
	if err != nil {
		return platform.SourceRef{}, "", err
	}
	defer unpin()
	return a.sourceRef, a.text, nil
}

func (s *inputSession) historyReadMaterials(ctx context.Context, h *historyQueries, q *historyQuery, offset, maximum int) (*historyPage, error) {
	body, err := s.historyReadPage(ctx, q, offset, maximum)
	if err != nil || body == nil {
		return nil, err
	}
	var out struct {
		Context  string                       `json:"context,omitempty"`
		Entries  []map[string]json.RawMessage `json:"entries"`
		Next     string                       `json:"next_cursor,omitempty"`
		Warnings []string                     `json:"warnings,omitempty"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	p := &historyPage{body: body}
	v := q.streams[0]
	end := len(v.locations) - offset
	var room RoomSnapshot
	if v.provider == "room" {
		var release func()
		room, release, err = s.historyRoom(&v.bound)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	// Reuse the normal saved-notice adapters. Latest records get image allowance
	// first; JSON entries retain chronological order.
	for i := len(out.Entries) - 1; i >= 0; i-- {
		l := v.locations[end-len(out.Entries)+i]
		var notices []materialNotice
		if v.provider == "room" {
			notices = roomInputNotices(s.service.info, []RoomMessage{room.Messages[l.Seq-room.ClearedThrough-1]})
		} else {
			entry, e := s.historyNotification(v, l)
			if e != nil {
				return nil, e
			}
			notices = s.notices([]InboxEntry{entry})
		}
		for j := range notices {
			n := &notices[j]
			n.request.Range = &attachmentRange{session: s, stream: v, location: l, sourceRef: n.source, text: n.text}
			n.request.SourceRef = ""
			if v.provider != "room" {
				n.request.Partition = l.Partition
			}
			if n.images.References > 0 {
				p.notices = append(p.notices, *n)
			}
		}
	}
	limits, err := s.service.imageMaterials.limits()
	if err != nil {
		return nil, err
	}
	limits.images = max(0, min(historyTurnImages, limits.images)-h.images)
	limits.totalBytes = max(int64(0), min(historyTurnImageBytes, limits.totalBytes)-h.imageBytes)
	budget := newNotificationImageBudget(limits)
	materials, accesses := budget.prepare(ctx, s.service.materialAttachments, attachmentReader{s.storage.id, s.execution.Turn}, p.notices, s.images)
	p.accesses = accesses
	// Attempts and actual raw bytes share one execution allowance across queries,
	// cursors and transports. Replays use the immutable cached result.
	h.images += budget.attempted
	h.imageBytes += budget.used
	type outcome struct {
		index  *int
		reason string
	}
	seen := map[string]outcome{}
	coverage := map[uint64][]historyImage{}
	for _, image := range materials {
		o := outcome{reason: image.reason}
		if len(image.data) > 0 {
			index := len(p.images) + 1
			p.images = append(p.images, mcp.NewImageContent(base64.StdEncoding.EncodeToString(image.data), image.mime))
			o.index = &index
		} else if image.shared != "" {
			o = seen[image.shared]
		}
		if o.index == nil && o.reason == "" {
			o.reason = "image was not supplied"
		}
		seen[imageMaterialLabel(image.seq, image.positions)] = o
		if len(image.positions) == 0 {
			coverage[image.seq] = append(coverage[image.seq], historyImage{Reason: o.reason})
		}
		for _, position := range image.positions {
			coverage[image.seq] = append(coverage[image.seq], historyImage{position, o.index, o.reason})
		}
	}
	for i := range out.Entries {
		l := v.locations[end-len(out.Entries)+i]
		if images := coverage[l.Seq]; len(images) > 0 {
			data, e := json.Marshal(images)
			if e != nil {
				return nil, e
			}
			out.Entries[i]["images"] = data
		}
	}
	// Material descriptors count against the same complete-record text limit.
	for {
		p.body, err = json.Marshal(out)
		if err != nil {
			return nil, err
		}
		if len(p.body) <= maximum {
			break
		}
		if len(out.Entries) <= 1 {
			if maximum < historyReadBytes {
				return nil, nil
			}
			return nil, errHistoryLarge
		}
		out.Entries = out.Entries[1:]
		out.Next = q.cursor(offset + len(out.Entries))
	}
	// Drop image blocks unused after the final text budget and remap
	// every index to the actual MCP content array.
	retained := map[int]int{}
	var images []mcp.ImageContent
	for _, entry := range out.Entries {
		var refs []historyImage
		_ = json.Unmarshal(entry["images"], &refs)
		for i := range refs {
			if refs[i].Index == nil {
				continue
			}
			old := *refs[i].Index
			index, ok := retained[old]
			if !ok {
				index = len(images) + 1
				retained[old] = index
				images = append(images, p.images[old-1])
			}
			refs[i].Index = &index
		}
		if len(refs) > 0 {
			entry["images"], _ = json.Marshal(refs)
		}
	}
	p.images = images
	for _, image := range images {
		data, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			return nil, err
		}
		p.imageBytes += int64(len(data))
	}
	p.body, err = json.Marshal(out)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(p.result())
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxMCPResponseBytes-(128<<10) {
		return nil, ErrCapacity
	}
	return p, nil
}

func (s *inputSession) checkHistoryMaterials(ctx context.Context, p *historyPage) error {
	// Unlike formal Inbox, local retained text does not depend on source admission
	// or a still-enabled subscription. Remote image account evidence is checked
	// only for supplied images; a missing image remains an explicit reason.
	m := s.service.materialAttachments
	if m == nil || len(p.accesses) == 0 || m.check == nil {
		return ctx.Err()
	}
	s.storage.store.mu.Lock()
	agent, err := s.storage.store.runningTurnLocked(s.storage.id, s.execution.Turn)
	s.storage.store.mu.Unlock()
	if err != nil {
		return err
	}
	return m.check(ctx, s.service.info, attachmentReader{s.storage.id, s.execution.Turn}, *agent.Turn, p.notices, p.accesses)
}
