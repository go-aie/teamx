package room

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
)

// Service policy, independent of notification capacity and frozen model config.
// Zero fields select defaults; hard ceilings preserve the component's limits.
type imageMaterialConfig struct {
	ScanEntries int    `toml:"scan_entries"`
	MaxImages   int    `toml:"max_images"`
	ImageBytes  int64  `toml:"image_bytes"`
	TotalBytes  int64  `toml:"total_bytes"`
	MaxPixels   int64  `toml:"max_pixels"`
	Timeout     string `toml:"timeout"`
}
type imageMaterialLimits struct {
	scan, images                   int
	imageBytes, totalBytes, pixels int64
	timeout                        time.Duration
}

func (c imageMaterialConfig) limits() (imageMaterialLimits, error) {
	l := imageMaterialLimits{25, 4, 2 << 20, 4 << 20, 16 << 20, 30 * time.Second}
	if c.ScanEntries != 0 {
		l.scan = c.ScanEntries
	}
	if c.MaxImages != 0 {
		l.images = c.MaxImages
	}
	if c.ImageBytes != 0 {
		l.imageBytes = c.ImageBytes
	}
	if c.TotalBytes != 0 {
		l.totalBytes = c.TotalBytes
	}
	if c.MaxPixels != 0 {
		l.pixels = c.MaxPixels
	}
	if c.Timeout != "" {
		var err error
		l.timeout, err = time.ParseDuration(c.Timeout)
		if err != nil {
			return l, fmt.Errorf("%w: invalid image material timeout", ErrRequest)
		}
	}
	if l.scan < 1 || l.scan > 100 || l.images < 1 || l.images > 16 || l.imageBytes < 1 || l.imageBytes > 4<<20 || l.totalBytes < 1 || l.totalBytes > 4<<20 || l.pixels < 1 || l.pixels > 64<<20 || l.timeout <= 0 || l.timeout > 2*time.Minute {
		return l, fmt.Errorf("%w: image material limits exceed service ceilings", ErrRequest)
	}
	return l, nil
}

type materialNotice struct {
	request attachmentRequest
	source  platform.SourceRef
	author  string
	created time.Time
	text    string
	images  notificationImageReferences
}

func (n materialNotice) label() string {
	channel := "Room"
	if n.request.AgentID != "" {
		channel = "Inbox"
	}
	return fmt.Sprintf("%s #%d", channel, n.request.Seq)
}

type materialAttachments struct {
	download   func(context.Context, attachmentRequest, *attachmentReader, attachment.ImageLimits) ([]byte, string, *attachmentAccess, error)
	check      func(context.Context, ServiceInfo, attachmentReader, TurnRecord, []materialNotice, []*attachmentAccess) error
	checkInbox func(context.Context, ServiceInfo, attachmentReader, []materialNotice) error
}

// Agent-independent image bytes and coverage. The MCP adapter owns encoding;
// platform attachment admission and validation remain shared with page previews.
type notificationImageMaterial struct {
	seq                    uint64
	positions              []int
	status, reason, shared string
	data                   []byte
	mime                   string
	access                 *attachmentAccess
}

type notificationImageKey struct{ provider, instance, subscription, channel, identity string }
type notificationImageBudget struct {
	limits             imageMaterialLimits
	scanned, attempted int
	used               int64
	spent              time.Duration
	seen               map[notificationImageKey]notificationImageMaterial
}

func newNotificationImageBudget(limits imageMaterialLimits) *notificationImageBudget {
	return &notificationImageBudget{limits: limits, seen: make(map[notificationImageKey]notificationImageMaterial)}
}

func (b *notificationImageBudget) recordCoverage(materials []notificationImageMaterial) {
	for key, previous := range b.seen {
		for _, image := range materials {
			if image.shared == "" && image.seq == previous.seq && slices.Equal(image.positions, previous.positions) {
				previous.status, previous.reason = image.status, image.reason
				b.seen[key] = previous
			}
		}
	}
}

func inboxImageNotices(info ServiceInfo, id MemberIdentity, entries []InboxEntry) []materialNotice {
	notices := make([]materialNotice, 0, len(entries))
	for _, e := range entries {
		if e.payloadType() != InboxPlatformNotification {
			continue
		}
		notices = append(notices, materialNotice{
			request: attachmentRequest{ServiceInfo: info, AgentID: id.AgentID, MembershipID: id.MembershipID, Seq: e.Seq},
			source:  e.Item.Source, text: e.Item.Text, images: findNotificationImages(e.Item.Text, e.Item.Source),
		})
	}
	return notices
}

func (b *notificationImageBudget) prepare(ctx context.Context, service *materialAttachments, reader attachmentReader, notices []materialNotice, images bool) ([]notificationImageMaterial, []*attachmentAccess) {
	started := time.Now()
	prep, cancel := context.WithTimeout(ctx, max(time.Nanosecond, b.limits.timeout-b.spent))
	defer func() { cancel(); b.spent += time.Since(started) }()
	var materials []notificationImageMaterial
	var accesses []*attachmentAccess
	for _, notice := range notices {
		b.scanned++
		for _, image := range notice.images.Images {
			item := notificationImageMaterial{seq: notice.request.Seq, positions: image.Positions}
			key := notificationImageKey{notice.source.Provider, notice.source.Instance, notice.source.SubscriptionID, notice.source.Channel, image.Identity}
			old, duplicate := b.seen[key]
			switch {
			case !image.Supported:
				item.status, item.reason = "unsupported", "incomplete or unsupported saved image reference"
			case !images:
				item.status, item.reason = "omitted", "current model is text-only"
			case duplicate:
				item.status, item.reason, item.access = old.status, old.reason, old.access
				item.shared = imageMaterialLabel(old.seq, old.positions)
			case b.scanned > b.limits.scan:
				item.status, item.reason = "omitted", "turn scan budget exceeded"
			case prep.Err() != nil:
				item.status, item.reason = "omitted", "turn preparation time budget exceeded"
			case b.attempted >= b.limits.images:
				item.status, item.reason = "omitted", "turn image count budget exceeded"
			case b.used >= b.limits.totalBytes:
				item.status, item.reason = "omitted", "turn image byte budget exceeded"
			case service == nil:
				item.status, item.reason = "unavailable", "attachment service unavailable"
			default:
				b.attempted++
				data, mime, access, err := service.download(prep, notice.requestWithPath(image.Path), &reader, attachment.ImageLimits{MaxBytes: min(b.limits.imageBytes, b.limits.totalBytes-b.used), MaxPixels: b.limits.pixels})
				switch {
				case err == nil:
					b.used += int64(len(data))
					item.status, item.data, item.mime, item.access = "supplied", data, mime, access
				case errors.Is(err, attachment.ErrLimit):
					item.status, item.reason = "omitted", "image byte/pixel or turn byte budget exceeded"
				case errors.Is(err, attachment.ErrFormat):
					item.status, item.reason = "unsupported", "invalid image format"
				case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
					item.status, item.reason = "unavailable", "download canceled or preparation timed out"
				default:
					item.status, item.reason = "unavailable", "attachment unavailable, permission denied or source binding changed"
				}
			}
			if item.access != nil {
				accesses = append(accesses, item.access)
			}
			if image.Supported && !duplicate {
				cached := item
				cached.data = nil // Supplied bytes live only in the cached MCP page.
				b.seen[key] = cached
			}
			materials = append(materials, item)
		}
		if notice.images.Omitted > 0 {
			materials = append(materials, notificationImageMaterial{seq: notice.request.Seq, status: "omitted", reason: "per-notification image reference limit exceeded"})
		}
	}
	return materials, accesses
}

func imageMaterialLabel(seq uint64, positions []int) string {
	return fmt.Sprintf("Inbox #%d image positions %v", seq, positions)
}

func (n materialNotice) requestWithPath(path string) attachmentRequest {
	req := n.request
	req.Path = path
	return req
}
