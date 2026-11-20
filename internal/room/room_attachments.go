package room

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	roomAttachmentBytes      int64 = 2 << 20
	roomAttachmentPixels     int64 = 16 << 20
	roomAttachmentTotalBytes int64 = 4 << 20
	roomAttachmentCount            = 4
	roomAttachmentQuota      int64 = 1 << 30
	roomAttachmentObjects          = 4096
	roomAttachmentTTL              = 24 * time.Hour
)

// Requests carry only an object identity. Descriptors come from private storage.
type RoomAttachmentInput struct {
	ID string `json:"id"`
}
type RoomAttachment struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}
type roomAttachmentMeta struct {
	RoomIdentity
	Operator   string         `json:"operator"`
	CreatedAt  time.Time      `json:"created_at"`
	SHA256     string         `json:"sha256"`
	Attachment RoomAttachment `json:"attachment"`
}
type roomAttachmentLimits struct {
	MaxImages       int      `json:"max_images"`
	ImageBytes      int64    `json:"image_bytes"`
	TotalBytes      int64    `json:"total_bytes"`
	MaxPixels       int64    `json:"max_pixels"`
	RoomBytes       int64    `json:"room_bytes"`
	RoomObjects     int      `json:"room_objects"`
	DraftTTLSeconds int64    `json:"draft_ttl_seconds"`
	MIMETypes       []string `json:"mime_types"`
}

func attachmentLimits(c imageMaterialConfig) roomAttachmentLimits {
	l, _ := c.limits()
	return roomAttachmentLimits{
		MaxImages: min(roomAttachmentCount, l.images), ImageBytes: min(roomAttachmentBytes, l.imageBytes, l.totalBytes),
		TotalBytes: min(roomAttachmentTotalBytes, l.totalBytes), MaxPixels: min(roomAttachmentPixels, l.pixels),
		RoomBytes: roomAttachmentQuota, RoomObjects: roomAttachmentObjects, DraftTTLSeconds: int64(roomAttachmentTTL / time.Second),
		MIMETypes: []string{"image/png", "image/jpeg", "image/webp"},
	}

}
func validRoomAttachmentID(id string) bool {
	if len(id) != 36 || !strings.HasPrefix(id, "img_") {
		return false
	}
	b, err := hex.DecodeString(id[4:])
	return err == nil && len(b) == 16 && strings.ToLower(id) == id
}
func cleanAttachmentName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(name, ""))
	for len(name) > 256 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" || name == "." || name == ".." {
		return "image"
	}
	return name
}
func validAttachmentInputs(a []RoomAttachmentInput) bool {
	if len(a) > roomAttachmentCount {
		return false
	}
	seen := map[string]bool{}
	for _, v := range a {
		if !validRoomAttachmentID(v.ID) || seen[v.ID] {
			return false
		}
		seen[v.ID] = true
	}
	return true
}
func validRoomAttachments(m RoomMessage) bool {
	if len(m.Attachments) == 0 {
		return true
	}
	if m.Author.Kind != "human" || m.Source != nil || m.Event != nil || len(m.Attachments) > roomAttachmentCount {
		return false
	}
	var total int64
	ids := []RoomAttachmentInput{}
	for _, a := range m.Attachments {
		if a.Kind != "image" || a.Name != cleanAttachmentName(a.Name) || a.Size <= 0 || a.Size > roomAttachmentBytes || a.Width <= 0 || a.Height <= 0 || int64(a.Width) > roomAttachmentPixels/int64(a.Height) || !slices.Contains([]string{"image/png", "image/jpeg", "image/webp"}, a.MIMEType) {
			return false
		}
		total += a.Size
		ids = append(ids, RoomAttachmentInput{a.ID})
	}
	return total <= roomAttachmentTotalBytes && validAttachmentInputs(ids)
}
func attachmentIDsEqual(a []RoomAttachment, b []RoomAttachmentInput) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}
func roomMessageBytes(m RoomMessage) int {
	n := len(m.Text)
	if len(m.Attachments) > 0 {
		b, _ := json.Marshal(m.Attachments)
		n += len(b)
	}
	return n
}
func attachmentError(message string) error { return fmt.Errorf("%w: %s", ErrRequest, message) }

func validRoomMessageText(text string, attachments int) bool {
	return utf8.ValidString(text) && len(text) <= maxTextBytes && (validText(text) || attachments > 0)
}
