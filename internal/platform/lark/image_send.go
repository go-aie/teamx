package lark

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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
)

// ImageRef identifies an original published image in the calling Room. It is
// never a path, URL, model content index or platform resource key.
type ImageRef struct {
	MessageSeq   uint64 `json:"message_seq"`
	AttachmentID string `json:"attachment_id"`
}
type ImageSendInput struct {
	Body   string     `json:"body,omitempty"`
	Images []ImageRef `json:"images,omitempty"`
}
type SendImage struct {
	Data         []byte
	MIME, SHA256 string
}
type PreparedImageMessage struct {
	Body   string
	Images []SendImage
}
type ImageSource func(context.Context, ImageSendInput) (PreparedImageMessage, error)
type ImageWriter func(context.Context, platform.TargetActionRequest, PreparedImageMessage) (string, []string, error)

const imageSendSchema = `{"type":"object","additionalProperties":false,"anyOf":[{"required":["body"]},{"required":["images"]}],"properties":{"body":{"type":"string","minLength":1,"maxLength":4096,"description":"准确 UTF-8 纯文本，最多 4096 字节；文字在前，原图按数组顺序在后，一次动作只发送一条消息"},"images":{"type":"array","minItems":1,"maxItems":4,"description":"当前 Room 已发布且仍保留的人类 PNG/JPEG/WebP 原图；每张最多 2 MiB、16 Mi 像素，合计最多 4 MiB；使用正式读取或 History 的 seq 和 attachments[].id，无需读取回执","items":{"type":"object","additionalProperties":false,"required":["message_seq","attachment_id"],"properties":{"message_seq":{"type":"integer","minimum":1},"attachment_id":{"type":"string","pattern":"^img_[0-9a-f]{32}$"}}}}}}`

func imageSendInput(raw json.RawMessage) (ImageSendInput, error) {
	var input ImageSendInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !utf8.Valid(raw) || d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return input, failure("invalid_send_input")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return input, failure("invalid_send_input")
	}
	for field := range fields {
		if field != "body" && field != "images" {
			return input, failure("invalid_send_input")
		}
	}
	if rawRefs, ok := fields["images"]; ok {
		var refs []map[string]json.RawMessage
		if json.Unmarshal(rawRefs, &refs) != nil {
			return input, failure("invalid_send_input")
		}
		for _, ref := range refs {
			if len(ref) != 2 || ref["message_seq"] == nil || ref["attachment_id"] == nil {
				return input, failure("invalid_send_input")
			}
		}
	}
	if body, ok := fields["body"]; ok {
		if bytes.Equal(body, []byte("null")) || !utf8.ValidString(input.Body) || strings.TrimSpace(input.Body) == "" || len(input.Body) > 4096 || strings.ContainsRune(input.Body, 0) {
			return input, failure("invalid_send_input")
		}
	}
	if _, ok := fields["images"]; ok && (len(input.Images) == 0 || len(input.Images) > 4) {
		return input, failure("invalid_send_input")
	}
	if input.Body == "" && len(input.Images) == 0 {
		return input, failure("invalid_send_input")
	}
	seen := map[string]bool{}
	for _, ref := range input.Images {
		if ref.MessageSeq == 0 || len(ref.AttachmentID) != 36 || !strings.HasPrefix(ref.AttachmentID, "img_") || strings.Trim(ref.AttachmentID[4:], "0123456789abcdef") != "" || seen[ref.AttachmentID] {
			return input, failure("invalid_send_input")
		}
		seen[ref.AttachmentID] = true
	}
	return input, nil
}

// ContentDigest freezes text and ordered actual image bytes, independently of
// source references, upload keys and JSON field order.
func (m PreparedImageMessage) ContentDigest() string {
	images := make([][]byte, len(m.Images))
	for i, img := range m.Images {
		images[i] = img.Data
	}
	return platform.ImageContentDigest(m.Body, images)
}

// VerifyContent checks a native read against the complete original message,
// including server-supplied image dimensions and any content_v2 mirror.
func (m PreparedImageMessage) VerifyContent(kind, content string, keys []string) bool {
	return verifyImageContent(kind, content, m.Body, keys, len(m.Images), m.Images...)
}

// WithImageActions adds Room image content to accepted self/group modes. The
// application catalog publishes these schemas; Room supplies the trusted source.
func WithImageActions(caps platform.ReferenceCapabilities, reader SendReader, source ImageSource, write ImageWriter, ready func(context.Context) error) platform.ReferenceCapabilities {
	for _, op := range []platform.Operation{platform.OperationMessageSend, platform.OperationMessageSendSelf} {
		cap, ok := caps.Actions[op]
		if !ok {
			continue
		}
		plainInspect, plainExecute := cap.Inspect, cap.Execute
		cap.Spec.InputSchema = json.RawMessage(imageSendSchema)
		inspect := func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
			input, err := imageSendInput(q.Input)
			if err != nil {
				return platform.TargetActionInspection{}, err
			}
			if len(input.Images) == 0 {
				return plainInspect(ctx, q, a)
			}
			if q.Operation != op || cap.Spec.DisabledReason != "" {
				return platform.TargetActionInspection{}, failure("operation_unavailable")
			}
			if op == platform.OperationMessageSendSelf {
				if !validSendTarget(q.Target, a) || q.Target.Object.Type != "user" || q.Target.Object.ID != a.Account.ID || len(q.Target.Containers) != 1 {
					return platform.TargetActionInspection{}, failure("self_target_required")
				}
			} else if q.Target.Object.Type != "chat" {
				return platform.TargetActionInspection{}, failure("user_send_acceptance_pending")
			}
			_, name, err := verifySendTarget(ctx, q.Target, a, reader)
			if err != nil {
				return platform.TargetActionInspection{}, err
			}
			if source == nil || ready == nil {
				return platform.TargetActionInspection{}, failure("image_send_unavailable")
			}
			if err := ready(ctx); err != nil {
				return platform.TargetActionInspection{}, err
			}
			message, err := source(ctx, input)
			if err != nil {
				return platform.TargetActionInspection{}, err
			}
			if message.Body != input.Body || len(message.Images) != len(input.Images) {
				return platform.TargetActionInspection{}, failure("image_source_mismatch")
			}
			return platform.TargetActionInspection{Target: q.Target, Account: a.Account, ContainerLabel: name, ContentDigest: message.ContentDigest()}, nil
		}
		cap.Inspect = inspect
		cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
			input, err := imageSendInput(q.Input)
			if err == nil && len(input.Images) == 0 {
				return plainExecute(ctx, q, a)
			}
			r := platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "not_applied", RetryClass: "never"}
			ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
			defer cancel()
			if err != nil {
				r.ReasonCode = "invalid_send_input"
				return r, nil
			}
			if _, err = inspect(ctx, q, a); err != nil {
				r.ReasonCode = ErrorCode(err)
				return r, nil
			}
			if write == nil || len(q.IdempotencyKey) != 32 || strings.Trim(q.IdempotencyKey, "0123456789abcdef") != "" {
				r.ReasonCode = "invalid_send_dispatch"
				return r, nil
			}
			message, err := source(ctx, input)
			if err != nil {
				r.ReasonCode = "image_source_unavailable"
				return r, nil
			}
			id, keys, err := write(ctx, q, message)
			if errors.Is(err, ErrSendNotSent) {
				r.ReasonCode = ErrorCode(err)
				if r.ReasonCode == "query_failed" {
					r.ReasonCode = "image_send_not_sent"
				}
				r.Message = "整条消息未发送；已上传但未引用的资源可能保留。"
				return r, nil
			}
			r.Status = "unknown"
			if nativeID(id, "om_") {
				r.ResultID = platform.ExternalID{Namespace: nativeNamespace(a.Account, "message_id"), Value: id}
			}
			if err != nil || !r.ResultID.Valid() {
				r.ReasonCode = "send_result_unknown"
				return r, nil
			}
			got, e := reader.Message(ctx, a.Account, id)
			chat := q.Target.Object.ID.Value
			if q.Target.Object.Type == "user" {
				chat = q.Target.Containers[0].ID.Value
			}
			if e != nil || !got.valid() || got.ID != id || got.Deleted || got.ChatID != chat || got.ParentID != "" || got.RootID != "" || got.ThreadID != "" || got.Sender.Type != "user" || got.Sender.IDType != "open_id" || got.Sender.ID != a.Account.ID.Value || got.Sender.TenantKey != a.Account.Tenant.Value || !message.VerifyContent(got.Type, got.Body.Content, keys) {
				r.ReasonCode = "send_verification_failed"
				return r, nil
			}
			r.Status = "succeeded"
			return r, nil
		}
		caps.Actions[op] = cap
	}
	return caps
}

type postNode struct {
	Tag      string `json:"tag"`
	Text     string `json:"text,omitempty"`
	ImageKey string `json:"image_key,omitempty"`
	Width    *int   `json:"width,omitempty"`
	Height   *int   `json:"height,omitempty"`
}

func (n *postNode) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || json.Unmarshal(fields["tag"], &n.Tag) != nil {
		return failure("invalid_post_node")
	}
	var field string
	switch n.Tag {
	case "text":
		field = "text"
		if style, ok := fields["style"]; ok {
			var styles []string
			if json.Unmarshal(style, &styles) != nil || styles == nil || len(styles) != 0 {
				return failure("invalid_post_node")
			}
			delete(fields, "style")
		}
	case "img":
		field = "image_key"
		if width, ok := fields["width"]; ok {
			if json.Unmarshal(width, &n.Width) != nil || json.Unmarshal(fields["height"], &n.Height) != nil || n.Width == nil || n.Height == nil || *n.Width <= 0 || *n.Height <= 0 || *n.Width > 12000 || *n.Height > 12000 {
				return failure("invalid_post_node")
			}
			delete(fields, "width")
			delete(fields, "height")
		}
	default:
		return failure("invalid_post_node")
	}
	if len(fields) != 2 {
		return failure("invalid_post_node")
	}
	raw, ok := fields[field]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return failure("invalid_post_node")
	}
	if n.Tag == "text" {
		return json.Unmarshal(raw, &n.Text)
	}
	return json.Unmarshal(raw, &n.ImageKey)
}

func (n postNode) MarshalJSON() ([]byte, error) {
	if n.Tag == "text" {
		return json.Marshal(map[string]string{"tag": n.Tag, "text": n.Text})
	}
	return json.Marshal(map[string]string{"tag": n.Tag, "image_key": n.ImageKey})
}

type postLocale struct {
	Title   string       `json:"title"`
	Content [][]postNode `json:"content"`
}

func imagePayload(body string, keys []string) (string, string, error) {
	if len(keys) == 0 || len(keys) > 4 {
		return "", "", failure("invalid_image_payload")
	}
	for _, key := range keys {
		if !imageKeyPattern.MatchString(key) {
			return "", "", failure("invalid_image_payload")
		}
	}
	if body == "" && len(keys) == 1 {
		b, _ := json.Marshal(map[string]string{"image_key": keys[0]})
		return "image", string(b), nil
	}
	rows := [][]postNode{}
	// One text row per line preserves newlines, including leading/trailing blank lines.
	if body != "" {
		for _, line := range strings.Split(body, "\n") {
			rows = append(rows, []postNode{{Tag: "text", Text: line}})
		}
	}
	for _, key := range keys {
		rows = append(rows, []postNode{{Tag: "img", ImageKey: key}})
	}
	b, _ := json.Marshal(map[string]postLocale{"zh_cn": {Content: rows}})
	return "post", string(b), nil
}

// Fail closed on nodes, locales or titles that the sender did not construct.
// Both wrapped zh_cn and the native unwrapped read representation are handled.
func verifyImageContent(kind, content, body string, keys []string, count int, images ...SendImage) bool {
	if len(keys) != count {
		return false
	}
	expectedKind, expected, err := imagePayload(body, keys)
	if err != nil || kind != expectedKind {
		return false
	}
	if kind == "image" {
		var got map[string]json.RawMessage
		var key string
		return json.Unmarshal([]byte(content), &got) == nil && len(got) == 1 && json.Unmarshal(got["image_key"], &key) == nil && key == keys[0]
	}
	var got map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &got) != nil {
		return false
	}
	if wrapped, ok := got["zh_cn"]; ok {
		if len(got) != 1 {
			return false
		}
		got = nil
		if json.Unmarshal(wrapped, &got) != nil {
			return false
		}
	}
	// Native post reads add a content_v2 mirror and image dimensions. Validate
	// both representations and bind dimensions to the actual original bytes.
	v2, hasV2 := got["content_v2"]
	delete(got, "content_v2")
	if len(got) != 2 || got["title"] == nil || got["content"] == nil || bytes.Equal(got["title"], []byte("null")) {
		return false
	}
	var actual postLocale
	d := json.NewDecoder(strings.NewReader(mustJSON(got)))
	d.DisallowUnknownFields()
	if d.Decode(&actual) != nil || actual.Title != "" {
		return false
	}
	validRows := func(rows [][]postNode) bool {
		index := 0
		for _, row := range rows {
			for _, node := range row {
				if node.Tag == "text" && node.ImageKey != "" || node.Tag == "img" && node.Text != "" || node.Tag != "text" && node.Tag != "img" {
					return false
				}
				if node.Tag == "img" {
					if node.Width != nil || node.Height != nil {
						if index >= len(images) || node.Width == nil || node.Height == nil {
							return false
						}
						config, _, err := image.DecodeConfig(bytes.NewReader(images[index].Data))
						if err != nil || config.Width != *node.Width || config.Height != *node.Height {
							return false
						}
					}
					index++
				}
			}
		}
		return index == count
	}
	if !validRows(actual.Content) {
		return false
	}
	if hasV2 {
		var rows [][]postNode
		if json.Unmarshal(v2, &rows) != nil || !validRows(rows) {
			return false
		}
		a, _ := json.Marshal(rows)
		b, _ := json.Marshal(actual.Content)
		if !bytes.Equal(a, b) {
			return false
		}
	}
	var want map[string]postLocale
	_ = json.Unmarshal([]byte(expected), &want)
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(want["zh_cn"])
	return bytes.Equal(a, b)
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func (p *Pinned) ImageUploadReady(ctx context.Context) error {
	permission, err := p.SendPermission(ctx)
	if err != nil {
		return err
	}
	for _, cap := range permission.Capabilities {
		if cap.Name == "image.upload" {
			if cap.State == "ready" {
				return nil
			}
			return failure("image_upload_" + cap.ReasonCode)
		}
	}
	return failure("image_upload_unchecked")
}

// All uploads finish before dispatch. Only the single messages POST crosses the
// uncertain-write boundary; uploading an unreferenced resource is not a message.
func (p *Pinned) SendImages(ctx context.Context, target platform.TargetIdentity, message PreparedImageMessage, key string, check func(context.Context) error, dispatch func(context.Context, string) error) (string, []string, error) {
	notSent := func() (string, []string, error) { return "", nil, ErrSendNotSent }
	if p == nil || p.client == nil || check == nil || dispatch == nil || !validSendIdentity(target, p.Account) || target.Object.Type == "user" && target.Object.ID != p.Account.ID || len(key) != 32 || strings.Trim(key, "0123456789abcdef") != "" || len(message.Images) == 0 || len(message.Images) > 4 {
		return notSent()
	}
	if message.Body != "" {
		raw, _ := json.Marshal(SendInput{Body: message.Body})
		if _, err := sendInput(raw); err != nil {
			return notSent()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	c := p.client
	ctx = context.WithValue(ctx, accountContextKey{}, p.Account.Instance+"|"+p.Account.ID.Namespace+"|"+p.Account.ID.Value)
	if target.Object.Type == "user" && target.Object.ID == p.Account.ID {
		release, err := c.selfSend.acquire(ctx)
		if err != nil {
			return notSent()
		}
		defer release()
	}
	permission, err := p.SendPermission(ctx)
	if err != nil || target.Object.Type == "chat" && !permission.GroupSendEnabled || target.Object.Type == "user" && (!permission.SelfSendEnabled || len(target.Containers) != 1) {
		return notSent()
	}
	uploadReady := slices.ContainsFunc(permission.Capabilities, func(v Capability) bool { return v.Name == "image.upload" && v.State == "ready" })
	if !uploadReady {
		return notSent()
	}
	dir, err := c.temporaryDirectory("teamx-lark-images-")
	if err != nil {
		return notSent()
	}
	defer os.RemoveAll(dir)
	var total int
	for i, img := range message.Images {
		total += len(img.Data)
		mime, e := attachment.ValidateImage(img.Data, attachment.ImageLimits{MaxBytes: 2 << 20, MaxPixels: 16 << 20})
		sum := sha256.Sum256(img.Data)
		config, _, configErr := image.DecodeConfig(bytes.NewReader(img.Data))
		if configErr != nil || config.Width > 12000 || config.Height > 12000 || e != nil || mime != img.MIME || hex.EncodeToString(sum[:]) != img.SHA256 || total > 4<<20 {
			return notSent()
		}
		ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[img.MIME]
		if ext == "" {
			return notSent()
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("image-%d.%s", i+1, ext)), img.Data, 0600); err != nil {
			return notSent()
		}
	}
	digest := message.ContentDigest()
	keys := []string{}
	for i, img := range message.Images {
		if check(ctx) != nil {
			return notSent()
		}
		ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[img.MIME]
		var output []byte
		err = c.guarded(ctx, p.Account, func(ctx context.Context) error {
			release, e := c.requests.acquire(ctx)
			if e != nil {
				return e
			}
			defer release()
			if e = check(ctx); e != nil {
				return e
			}
			output, e = c.runInDir(ctx, dir, "im", "images", "create", "--data", `{"image_type":"message"}`, "--file", fmt.Sprintf("image=./image-%d.%s", i+1, ext), "--as", "user", "--format", "json")
			return e
		})
		var wire struct {
			OK       bool   `json:"ok"`
			Identity string `json:"identity"`
			Data     struct {
				Key string `json:"image_key"`
			} `json:"data"`
		}
		if err != nil || decode(output, &wire) != nil || !wire.OK || wire.Identity != "user" || !imageKeyPattern.MatchString(wire.Data.Key) {
			return "", nil, fmt.Errorf("%w: %w", ErrSendNotSent, failure(fmt.Sprintf("image_upload_%d_failed", i+1)))
		}
		keys = append(keys, wire.Data.Key)
	}
	kind, content, err := imagePayload(message.Body, keys)
	if err != nil {
		return notSent()
	}
	// Target reads acquire the client lock; finish these before the final locked POST.
	if target.Object.Type == "chat" {
		if _, err := c.SendGroup(ctx, p.Account, target.Object.ID.Value); err != nil {
			return notSent()
		}
	} else {
		chat, err := c.SelfChat(ctx, p.Account)
		if err != nil || !chat.Relation.ValidFor(p.Account, chat.ID) || chat.Relation.Kind != "self" || chat.ID != target.Containers[0].ID.Value {
			return notSent()
		}
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return notSent()
	}
	defer unlock()
	validation, err := c.verify(ctx)
	if err != nil || validation.Account != p.Account {
		return notSent()
	}
	release, err := c.requests.acquire(ctx)
	if err != nil {
		return notSent()
	}
	defer release()
	receiveType := "chat_id"
	if target.Object.Type == "user" {
		receiveType = "open_id"
	}
	params := mustJSON(map[string]string{"receive_id_type": receiveType})
	payload := mustJSON(map[string]string{"receive_id": target.Object.ID.Value, "msg_type": kind, "content": content, "uuid": key})
	if ctx.Err() != nil || check(ctx) != nil {
		return notSent()
	}
	if dispatch(ctx, digest) != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrSendNotSent, failure("image_dispatch_rejected"))
	}
	output, err := c.run(ctx, "api", "POST", "/open-apis/im/v1/messages", "--params", params, "--data", payload, "--as", "user", "--format", "json")
	if err != nil {
		return "", keys, err
	}
	var wire struct {
		OK       bool   `json:"ok"`
		Identity string `json:"identity"`
		Data     struct {
			ID string `json:"message_id"`
		} `json:"data"`
	}
	if decode(output, &wire) != nil || !wire.OK || wire.Identity != "user" || !nativeID(wire.Data.ID, "om_") {
		return "", keys, failure("invalid_response")
	}
	return wire.Data.ID, keys, nil
}
