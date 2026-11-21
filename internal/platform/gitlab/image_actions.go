package gitlab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type ImageRef struct {
	MessageSeq   uint64 `json:"message_seq"`
	AttachmentID string `json:"attachment_id"`
}
type ImageActionInput struct {
	Body   string     `json:"body,omitempty"`
	Images []ImageRef `json:"images,omitempty"`
}
type ActionImage struct {
	Data         []byte
	MIME, SHA256 string
}
type PreparedImageComment struct {
	Body   string
	Images []ActionImage
}

func (m PreparedImageComment) ContentDigest() string {
	images := make([][]byte, len(m.Images))
	for i, img := range m.Images {
		images[i] = img.Data
	}
	return platform.ImageContentDigest(m.Body, images)
}

type ImageSource func(context.Context, ImageActionInput) (PreparedImageComment, error)
type ImageUploadAPI func(context.Context, string, int, ActionImage, string) (string, error)

// ImageNoteAPI must run the final original-request check after acquiring the
// current credential permit, immediately before invoking the note transport.
type ImageNoteAPI func(context.Context, platform.TargetActionRequest, string, string) (APIResponse, error)

type ImageInputError struct{ Field, Expect string }

func (e *ImageInputError) Error() string         { return e.Field + ": " + e.Expect }
func imageInputError(field, expect string) error { return &ImageInputError{field, expect} }

const imageCommentSchema = `{"type":"object","description":"纯图片仅提交 images，完全省略 body，不传空字符串。图文 body 保留原说明，图片 Markdown 由服务端生成。","additionalProperties":false,"anyOf":[{"required":["body"]},{"required":["images"]}],"properties":{"body":{"type":"string","minLength":1,"maxLength":4096,"description":"用户要求的准确原始说明，非空且最多 4096 UTF-8 字节；不追加 Room 图片链接或附件标签；拒绝独立 quick action 命令行和吞图的未闭合块"},"images":{"type":"array","minItems":1,"maxItems":4,"description":"当前 Room 已发布且保留的人类 PNG/JPEG/WebP 原图；每张 2 MiB/16 Mi 像素，合计 4 MiB；数组顺序，整条一次提交。含图须当前 policy_token 及 authority:{type:human_reply,room_seq:真实授权指令序号,reason:解释}；最多一次尝试，不接受规则/持久指令/问答授权","items":{"type":"object","additionalProperties":false,"required":["message_seq","attachment_id"],"properties":{"message_seq":{"type":"integer","minimum":1},"attachment_id":{"type":"string","pattern":"^img_[0-9a-f]{32}$"}}}}}}`

// DecodeImageActionInput is shared by per-call strategy selection and execution.
// It never falls back to the old text contract after malformed image input.
func DecodeImageActionInput(raw json.RawMessage) (ImageActionInput, error) {
	var input ImageActionInput
	bad := func(field, expect string) (ImageActionInput, error) { return input, imageInputError(field, expect) }
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 32<<10 || !utf8.Valid(raw) || d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return bad("input", "expected body and/or images with only declared fields; see input_schema")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return bad("input", "expected an object")
	}
	if b, ok := fields["body"]; ok && (bytes.Equal(bytes.TrimSpace(b), []byte("null")) || !actionText(input.Body)) {
		return bad("input.body", "expected nonempty UTF-8 Markdown, no NUL, at most 4096 bytes")
	}
	if _, ok := fields["images"]; ok && (len(input.Images) < 1 || len(input.Images) > 4) {
		return bad("input.images", "expected 1-4 distinct Room image references; null and empty arrays are invalid")
	}
	if input.Body == "" && len(input.Images) == 0 {
		return bad("input", "provide body and/or images")
	}
	seen := map[string]bool{}
	for i, ref := range input.Images {
		path := fmt.Sprintf("input.images[%d].", i)
		if ref.MessageSeq == 0 {
			return bad(path+"message_seq", "expected positive integer Room message seq")
		}
		if len(ref.AttachmentID) != 36 || !strings.HasPrefix(ref.AttachmentID, "img_") || strings.Trim(ref.AttachmentID[4:], "0123456789abcdef") != "" || seen[ref.AttachmentID] {
			return bad(path+"attachment_id", "expected a distinct img_ followed by 32 lowercase hex digits from Room attachments[].id")
		}
		seen[ref.AttachmentID] = true
	}
	return input, nil
}

var quickActionLine = regexp.MustCompile(`^/[A-Za-z][A-Za-z0-9_-]*$`)

func imageMarkdown(body string, paths []string) (string, error) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && quickActionLine.MatchString(fields[0]) {
			return "", imageInputError("input.body", "standalone GitLab quick actions are not allowed with images; use an inline explanation")
		}
	}
	result := body
	appendStart := len(result)
	for i, path := range paths {
		if len(path) > 4096 || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\r\n\x00\t ()[]<>\"'`?#%") {
			return "", platform.ErrReference
		}
		if result != "" {
			result += "\n\n"
		}
		result += fmt.Sprintf("![image-%d](%s)", i+1, path)
	}
	if !actionTextLimit(result, 8192) {
		return "", imageInputError("input.body", "generated comment exceeds 8192 bytes")
	}
	// Parse the full body. Check the appended paragraphs by their source ranges;
	// user images cannot impersonate generated nodes, even with identical paths.
	source := []byte(result)
	doc := goldmark.DefaultParser().Parse(text.NewReader(source))
	index := 0
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		img, ok := n.(*ast.Image)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		block := n.Parent()
		for block != nil && block.Type() != ast.TypeBlock {
			block = block.Parent()
		}
		if block == nil || block.Lines().Len() == 0 || block.Lines().At(0).Start < appendStart {
			return ast.WalkContinue, nil
		}
		if index >= len(paths) || string(img.Destination) != paths[index] || string(img.Text(source)) != fmt.Sprintf("image-%d", index+1) {
			index = len(paths) + 1
			return ast.WalkStop, nil
		}
		index++
		return ast.WalkContinue, nil
	})
	if index != len(paths) {
		return "", imageInputError("input.body", "Markdown hides appended images; close code/HTML blocks before sending")
	}
	return result, nil
}
func placeholderPaths(count int) []string {
	paths := make([]string, count)
	for i := range paths {
		paths[i] = fmt.Sprintf("/uploads/00000000000000000000000000000000/image-%d.png", i+1)
	}
	return paths
}
func validateImageComment(m PreparedImageComment, input ImageActionInput) error {
	if m.Body != input.Body || len(m.Images) != len(input.Images) || len(m.Images) == 0 {
		return platform.ErrReference
	}
	total := 0
	for _, img := range m.Images {
		total += len(img.Data)
		mime, err := attachment.ValidateImage(img.Data, attachment.ImageLimits{MaxBytes: 2 << 20, MaxPixels: 16 << 20})
		hash := sha256.Sum256(img.Data)
		if err != nil || mime != img.MIME || imageExtension(mime) == "" || hex.EncodeToString(hash[:]) != img.SHA256 || total > 4<<20 {
			return platform.ErrReference
		}
	}
	return nil
}

// ImageReferenceActions is the managed MR comment/reply implementation. Plain
// text retains ReferenceActions' authorization and retry policy.
// Empty executionInput means discovery only; no source preparation or upload.
func ImageReferenceActions(read ReadAPI, plainWrite NoteAPI, write ImageNoteAPI, upload ImageUploadAPI, source ImageSource, check func(context.Context) error, username, executionInput string) (map[platform.Operation]platform.ActionCapability, error) {
	actions := ReferenceActions(read, plainWrite, username)
	selected := ImageActionInput{}
	if executionInput != "" {
		var err error
		selected, err = DecodeImageActionInput([]byte(executionInput))
		if err != nil {
			return nil, err
		}
	}
	for op, cap := range actions {
		cap.Spec.InputSchema = json.RawMessage(imageCommentSchema)
		if len(selected.Images) == 0 {
			actions[op] = cap
			continue
		}
		cap.Spec.DirectRoomInstructionOnly = true
		cap.Spec.Retry = platform.RetryPolicy{MaxAttempts: 1}
		prepare := func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (referenceAction, platform.TargetActionInspection, PreparedImageComment, error) {
			var inspected platform.TargetActionInspection
			var message PreparedImageComment
			input, err := DecodeImageActionInput(q.Input)
			if err != nil {
				return referenceAction{}, inspected, message, err
			}
			if q.Operation != op || !reflect.DeepEqual(input, selected) {
				return referenceAction{}, inspected, message, platform.ErrReference
			}
			native, err := actionNativeTarget(q, a)
			if err != nil {
				return native, inspected, message, err
			}
			if _, err = imageMarkdown(input.Body, placeholderPaths(len(input.Images))); err != nil {
				return native, inspected, message, err
			}
			if source == nil || check == nil {
				return native, inspected, message, platform.ErrReference
			}
			if err = check(ctx); err != nil {
				return native, inspected, message, err
			}
			inspected, err = inspectAction(ctx, read, native, q.Target, a, username)
			if err != nil {
				return native, inspected, message, err
			}
			message, err = source(ctx, input)
			if err == nil {
				err = validateImageComment(message, input)
			}
			if err == nil {
				inspected.ContentDigest = message.ContentDigest()
			}
			return native, inspected, message, err
		}
		cap.Inspect = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
			_, i, _, err := prepare(ctx, q, a)
			return i, err
		}
		cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
			r := platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "not_applied", RetryClass: "never", Message: "整条评论未发送；已上传但未引用的项目附件可能保留。"}
			native, inspected, message, err := prepare(ctx, q, a)
			if err != nil || upload == nil || write == nil || len(q.IdempotencyKey) != 32 || strings.Trim(q.IdempotencyKey, "0123456789abcdef") != "" {
				r.ReasonCode = "image_preflight_failed"
				return r, nil
			}
			paths := make([]string, 0, len(message.Images))
			for i, img := range message.Images {
				if check(ctx) != nil {
					r.ReasonCode = "image_source_changed"
					return r, nil
				}
				path, e := upload(ctx, native.projectID, i+1, img, inspected.ContainerLabel)
				if e != nil {
					r.ReasonCode = "image_upload_failed"
					var uploadError *imageUploadError
					if errors.As(e, &uploadError) {
						r.ReasonCode = uploadError.code
						r.Message = uploadError.message + r.Message
					}
					return r, nil
				}
				paths = append(paths, path)
			}
			body, err := imageMarkdown(message.Body, paths)
			if err != nil || check(ctx) != nil {
				r.ReasonCode = "image_content_rejected"
				return r, nil
			}
			// Recheck the precise native target after all uploads, before the final permit.
			verified, err := inspectAction(ctx, read, native, q.Target, a, username)
			if err != nil || verified.AccountLabel != inspected.AccountLabel || verified.ContainerLabel != inspected.ContainerLabel || verified.URL != inspected.URL {
				r.ReasonCode = "image_target_changed"
				return r, nil
			}
			response, err := write(ctx, q, message.ContentDigest(), body)
			if err != nil {
				r.Status = "unknown"
				r.ReasonCode = "note_unconfirmed"
				var failure *noteError
				if errors.Is(err, ErrNoteNotSent) || errors.As(err, &failure) && failure.notApplied {
					r.Status = "not_applied"
					r.ReasonCode = "note_not_sent"
				}
				return r, nil
			}
			r.Status = "unknown"
			r.ReasonCode = "note_verification_failed"
			var note referenceNote
			if decodeJSON(response.Data, &note) != nil {
				return r, nil
			}
			if note.ID > 0 {
				r.ResultID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: strconv.FormatInt(note.ID, 10)}
				r.URL = inspected.URL + "#note_" + r.ResultID.Value
				if len(r.URL) > 4096 {
					r.URL = ""
				}
			}
			native.body = body
			if !validActionNote(note, native, inspected.AccountLabel) {
				return r, nil
			}
			var saved referenceNote
			if actionGet(ctx, read, native.mrPath()+"/notes/"+r.ResultID.Value, &saved) != nil || saved.ID != note.ID || !validActionNote(saved, native, inspected.AccountLabel) {
				return r, nil
			}
			if native.discussionID != "" {
				var d actionDiscussion
				if actionGet(ctx, read, native.discussionPath(), &d) != nil || d.ID != native.discussionID || d.Individual {
					return r, nil
				}
				found := false
				for _, n := range d.Notes {
					if n.ID == note.ID && validActionNote(n, native, inspected.AccountLabel) {
						found = true
					}
				}
				if !found {
					return r, nil
				}
			}
			r.Status = "succeeded"
			r.ReasonCode = ""
			r.Message = "GitLab 独立回读已核实完整评论及图片顺序。"
			return r, nil
		}
		actions[op] = cap
	}
	return actions, nil
}
