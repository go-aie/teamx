package lark

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

const ImagePrefix = "lark-image:"
const MaxImageReferences = 100

var imageKeyPattern = regexp.MustCompile(`^img_[A-Za-z0-9_-]{1,252}$`)
var legacyImageLine = regexp.MustCompile(`(?m)^image_key: (img_[A-Za-z0-9_-]{1,252})\r?$`)

// ImageKeys reads only native image messages and rich-post image elements, never
// textual URLs or resource markers supplied by a sender in ordinary text.
func (m Message) ImageKeys() []string {
	if m.Deleted || m.Type != "image" && m.Type != "post" {
		return nil
	}
	var body map[string]any
	if json.Unmarshal([]byte(m.Body.Content), &body) != nil {
		return nil
	}
	var keys []string
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 20 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			if key, ok := x["image_key"].(string); ok && imageKeyPattern.MatchString(key) && (m.Type == "image" && depth == 0 || m.Type == "post" && x["tag"] == "img") {
				keys = append(keys, key)
			}
			for _, k := range []string{"content", "zh_cn", "en_us", "ja_jp"} {
				walk(x[k], depth+1)
			}
		case []any:
			for _, item := range x {
				walk(item, depth+1)
			}
		}
	}
	walk(body, 0)
	return keys
}

func ImageMessage(source platform.SourceRef) (string, bool) {
	r := source.Reference
	id := r.Target.ID.Value
	return id, source.Provider == "lark" && source.Channel == "messages" && (source.Instance == Origin("feishu") || source.Instance == Origin("lark")) && r.Valid() && r.Version == 2 && r.Target.Type == "message" && strings.HasPrefix(r.Target.ID.Namespace, "lark:message_id:cli_") && nativeID(id, "om_")
}

type NotificationImage struct {
	Key       string
	Positions []int
}
type NotificationImages struct {
	Images     []NotificationImage
	References int
	Omitted    int
}

// Discovery is not authority to download: Image rechecks the owning message.
// Legacy resource lines keep already delivered Inbox entries previewable.
func FindNotificationImages(saved string, source platform.SourceRef, limit int) NotificationImages {
	var result NotificationImages
	if _, ok := ImageMessage(source); !ok {
		return result
	}
	limit = max(0, min(limit, MaxImageReferences))
	seen := map[string]int{}
	add := func(key string) {
		if !imageKeyPattern.MatchString(key) {
			return
		}
		result.References++
		if result.References > limit {
			result.Omitted++
			return
		}
		if i, ok := seen[key]; ok {
			result.Images[i].Positions = append(result.Images[i].Positions, result.References)
		} else {
			seen[key] = len(result.Images)
			result.Images = append(result.Images, NotificationImage{Key: key, Positions: []int{result.References}})
		}
	}
	input := []byte(saved)
	doc := goldmark.DefaultParser().Parse(text.NewReader(input))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); entering && ok && strings.HasPrefix(string(img.Destination), ImagePrefix) {
			add(strings.TrimPrefix(string(img.Destination), ImagePrefix))
		}
		return ast.WalkContinue, nil
	})
	if result.References == 0 {
		for _, match := range legacyImageLine.FindAllStringSubmatch(saved, -1) {
			add(match[1])
		}
	}
	return result
}

// Image downloads only a resource still present in the exact saved message.
// The official CLI writes into a private temporary directory. A size monitor
// cancels oversized transfers (including CLI temporary files); all files are
// removed after the process settles. Returned memory is strictly byte-bounded.
func (c *Client) Image(ctx context.Context, account platform.AccountIdentity, source platform.SourceRef, key string, limits attachment.ImageLimits) ([]byte, string, error) {
	id, ok := ImageMessage(source)
	if !ok || !imageKeyPattern.MatchString(key) || source.Instance != account.Instance || source.Reference.Target.ID.Namespace != nativeNamespace(account, "message_id") || source.Reference.ChatID.Namespace != nativeNamespace(account, "chat_id") || source.Reference.RecipientID != account.ID || source.Reference.AccountTenant != account.Tenant {
		return nil, "", attachment.ErrUnavailable
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = attachment.MaxBytes
	}
	if limits.MaxPixels == 0 {
		limits.MaxPixels = 16 << 20
	}
	if limits.MaxBytes < 1 || limits.MaxBytes > attachment.MaxBytes || limits.MaxPixels < 1 {
		return nil, "", attachment.ErrLimit
	}
	var data []byte
	var mime string
	err := c.guarded(ctx, account, func(ctx context.Context) error {
		var wire struct {
			Items []Message `json:"items"`
		}
		if err := c.api(ctx, "/open-apis/im/v1/messages/"+id, struct{}{}, &wire); err != nil {
			return err
		}
		if len(wire.Items) != 1 || !wire.Items[0].valid() || wire.Items[0].ID != id || wire.Items[0].ChatID != source.Reference.ChatID.Value {
			return attachment.ErrUnavailable
		}
		found := false
		for _, imageKey := range wire.Items[0].ImageKeys() {
			found = found || imageKey == key
		}
		if !found {
			return attachment.ErrUnavailable
		}
		dir, err := os.MkdirTemp("", "teamx-lark-image-")
		if err != nil {
			return attachment.ErrUnavailable
		}
		defer os.RemoveAll(dir)
		// macOS temporary roots can be symlinks; the CLI reports a resolved path.
		dir, err = filepath.EvalSymlinks(dir)
		if err != nil {
			return attachment.ErrUnavailable
		}
		limited, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		done := make(chan struct{})
		joined := make(chan struct{})
		go func() {
			defer close(joined)
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-limited.Done():
					return
				case <-tick.C:
					var size int64
					entries, _ := os.ReadDir(dir)
					for _, entry := range entries {
						if info, e := entry.Info(); e == nil {
							size += info.Size()
						}
					}
					if size > limits.MaxBytes {
						cancel(attachment.ErrLimit)
						return
					}
				}
			}
		}()
		release, err := c.requests.acquire(limited)
		var output []byte
		if err == nil {
			output, err = c.runInDir(limited, dir, "im", "+messages-resources-download", "--message-id", id, "--file-key", key, "--type", "image", "--output", "image.bin", "--as", "user", "--format", "json")
			release()
		}
		close(done)
		<-joined
		if errors.Is(context.Cause(limited), attachment.ErrLimit) {
			return attachment.ErrLimit
		}
		if err != nil {
			return err
		}
		var result struct {
			OK       bool   `json:"ok"`
			Identity string `json:"identity"`
			Data     struct {
				SavedPath string `json:"saved_path"`
				Size      int64  `json:"size_bytes"`
			} `json:"data"`
		}
		path := filepath.Join(dir, "image.bin")
		if decode(output, &result) != nil || !result.OK || result.Identity != "user" || (result.Data.SavedPath != "image.bin" && result.Data.SavedPath != path) || result.Data.Size < 1 {
			return failure("invalid_response")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != result.Data.Size {
			return failure("invalid_response")
		}
		if info.Size() > limits.MaxBytes {
			return attachment.ErrLimit
		}
		file, err := os.Open(path)
		if err != nil {
			return attachment.ErrUnavailable
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, limits.MaxBytes+1))
		if err != nil {
			return attachment.ErrUnavailable
		}
		mime, err = attachment.ValidateImage(data, limits)
		return err
	})
	if err != nil {
		return nil, "", errors.Join(attachment.ErrUnavailable, err)
	}
	return data, mime, nil
}
