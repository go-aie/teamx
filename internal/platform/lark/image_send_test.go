package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestImageSendNativePostMetadata(t *testing.T) {
	images := []SendImage{}
	for _, size := range [][2]int{{3, 2}, {2, 3}} {
		var data bytes.Buffer
		if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, size[0], size[1]))); err != nil {
			t.Fatal(err)
		}
		images = append(images, SendImage{Data: data.Bytes(), MIME: "image/png"})
	}
	keys := []string{"img_first", "img_second"}
	for _, body := range []string{"", "中文\n\n**literal**"} {
		build := func() map[string]any {
			_, raw, _ := imagePayload(body, keys)
			var wrapped map[string]any
			_ = json.Unmarshal([]byte(raw), &wrapped)
			post := wrapped["zh_cn"].(map[string]any)
			index := 0
			for _, row := range post["content"].([]any) {
				node := row.([]any)[0].(map[string]any)
				if node["tag"] == "text" {
					node["style"] = []any{}
					continue
				}
				config, _, _ := image.DecodeConfig(bytes.NewReader(images[index].Data))
				node["width"], node["height"] = config.Width, config.Height
				index++
			}
			encoded, _ := json.Marshal(post["content"])
			var mirror any
			_ = json.Unmarshal(encoded, &mirror)
			post["content_v2"] = mirror
			return post
		}
		valid := build()
		raw, _ := json.Marshal(valid)
		if !verifyImageContent("post", string(raw), body, keys, 2, images...) || verifyImageContent("post", string(raw), body, keys, 2) {
			t.Fatal("native mirror/dimensions must match the original images", body)
		}
		mutations := map[string]func(map[string]any){
			"wrong_width": func(p map[string]any) {
				p["content"].([]any)[len(p["content"].([]any))-2].([]any)[0].(map[string]any)["width"] = 99
			},
			"missing_height": func(p map[string]any) {
				delete(p["content"].([]any)[len(p["content"].([]any))-2].([]any)[0].(map[string]any), "height")
			},
			"negative_width": func(p map[string]any) {
				p["content"].([]any)[len(p["content"].([]any))-2].([]any)[0].(map[string]any)["width"] = -3
			},
			"extra_node_field": func(p map[string]any) {
				p["content"].([]any)[0].([]any)[0].(map[string]any)["href"] = "https://unexpected.test"
			},
			"extra_root_field":     func(p map[string]any) { p["extra"] = "unexpected" },
			"mirror_missing_image": func(p map[string]any) { rows := p["content_v2"].([]any); p["content_v2"] = rows[:len(rows)-1] },
			"mirror_changed_key": func(p map[string]any) {
				rows := p["content_v2"].([]any)
				rows[len(rows)-1].([]any)[0].(map[string]any)["image_key"] = "img_other"
			},
			"mirror_extra_text": func(p map[string]any) {
				p["content_v2"] = append(p["content_v2"].([]any), []any{map[string]any{"tag": "text", "text": "extra"}})
			},
			"mirror_wrong_dimension": func(p map[string]any) {
				rows := p["content_v2"].([]any)
				rows[len(rows)-1].([]any)[0].(map[string]any)["height"] = 8
			},
			"mirror_reordered": func(p map[string]any) {
				rows := p["content_v2"].([]any)
				rows[len(rows)-1], rows[len(rows)-2] = rows[len(rows)-2], rows[len(rows)-1]
			},
		}
		if body != "" {
			mutations["changed_style"] = func(p map[string]any) { p["content"].([]any)[0].([]any)[0].(map[string]any)["style"] = []any{"bold"} }
			mutations["null_style"] = func(p map[string]any) { p["content"].([]any)[0].([]any)[0].(map[string]any)["style"] = nil }
		}
		for name, mutate := range mutations {
			t.Run(body+"/"+name, func(t *testing.T) {
				post := build()
				mutate(post)
				raw, _ := json.Marshal(post)
				if verifyImageContent("post", string(raw), body, keys, 2, images...) {
					t.Fatal("changed native content was accepted")
				}
			})
		}
	}
}

func TestImageSendInputAndTextAdapterBoundary(t *testing.T) {
	ref := `{"message_seq":12,"attachment_id":"img_0123456789abcdef0123456789abcdef"}`
	for _, raw := range []string{`{}`, `null`, `{"body":""}`, `{"body":null}`, `{"body":" "}`, `{"images":[]}`, `{"images":null}`, `{"body":"ok","images":[]}`, `{"images":[` + ref + `,` + ref + `]}`, `{"images":[{"message_seq":0,"attachment_id":"img_0123456789abcdef0123456789abcdef"}]}`, `{"images":[{"message_seq":12,"attachment_id":"/tmp/file.png"}]}`, `{"images":[{"message_seq":12,"attachment_id":"img_0123456789abcdef0123456789abcdef","url":"https://example.test/x"}]}`, `{"body":"x","path":"file.png"}`, `{"images":[` + strings.Join([]string{ref, ref, ref, ref, ref}, ",") + `]}`, `{"body":"\u0000"}`, `{"body":"` + strings.Repeat("x", 4097) + `"}`} {
		if _, err := imageSendInput(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid input: %s", raw)
		}
	}
	for _, raw := range []string{`{"body":"literal **Markdown** @人\n\n最后\n"}`, `{"images":[` + ref + `]}`, `{"body":"说明","images":[` + ref + `]}`} {
		if _, err := imageSendInput(json.RawMessage(raw)); err != nil {
			t.Fatal(raw, err)
		}
	}
	f, a, q := sendFixture(t, "chat")
	q.Input = json.RawMessage(`{"images":[` + ref + `]}`)
	caps := AcceptedActionCapabilities(f, nil, nil)
	for _, op := range []platform.Operation{platform.OperationMessageSend, platform.OperationMessageSendSelf, platform.OperationMessageReply} {
		cap := caps.Actions[op]
		if strings.Contains(string(cap.Spec.InputSchema), `"images"`) {
			t.Fatal("text adapter schema opened images", op)
		}
	}
	if _, err := caps.Actions[platform.OperationMessageSend].Inspect(context.Background(), q, a); err == nil {
		t.Fatal("text adapter admitted images")
	}
}

func TestImageSendExactContentVerification(t *testing.T) {
	for _, body := range []string{"", "中文\n\n**literal** @./path\n", "\n开始", "no newline"} {
		for _, keys := range [][]string{{"img_first"}, {"img_first", "img_second"}} {
			kind, payload, err := imagePayload(body, keys)
			if err != nil || !verifyImageContent(kind, payload, body, keys, len(keys)) {
				t.Fatal(body, kind, payload, err)
			}
			if kind == "post" {
				var locales map[string]json.RawMessage
				_ = json.Unmarshal([]byte(payload), &locales)
				if !verifyImageContent(kind, string(locales["zh_cn"]), body, keys, len(keys)) {
					t.Fatal("native locale rejected")
				}
			}
			for _, bad := range []string{strings.Replace(payload, "img_first", "img_other", 1), `{"zh_cn":{"title":"unexpected","content":[]}}`, strings.Replace(payload, `"tag":"img"`, `"tag":"a"`, 1), strings.Replace(payload, `"zh_cn"`, `"en_us"`, 1)} {
				if bad != payload && verifyImageContent(kind, bad, body, keys, len(keys)) {
					t.Fatal("changed content verified", bad)
				}
			}
			if len(keys) == 2 && verifyImageContent(kind, payload, body, []string{keys[1], keys[0]}, 2) {
				t.Fatal("reordered images verified")
			}
			if verifyImageContent(kind, payload, body+"extra", keys, len(keys)) {
				t.Fatal("changed body verified")
			}
		}
	}
	for _, payload := range []string{
		`{"zh_cn":{"title":"","content":[[{"tag":"img","image_key":"img_first","href":"https://x"}]]}}`,
		`{"zh_cn":{"title":"","content":[[{"tag":"img","image_key":"img_first","text":"extra"}]]}}`,
		`{"zh_cn":{"title":"","content":[[{"tag":"img","image_key":"img_first"}]]},"en_us":{"title":"","content":[]}}`,
	} {
		if verifyImageContent("post", payload, "", []string{"img_first", "img_second"}, 2) {
			t.Fatal(payload)
		}
	}
	m := PreparedImageMessage{Body: "a", Images: []SendImage{{Data: []byte("one")}, {Data: []byte("two")}}}
	digest := m.ContentDigest()
	m.Images[0], m.Images[1] = m.Images[1], m.Images[0]
	if digest == m.ContentDigest() {
		t.Fatal("digest ignored image order")
	}
	m.Images[0], m.Images[1] = m.Images[1], m.Images[0]
	m.Body += "changed"
	if digest == m.ContentDigest() {
		t.Fatal("digest ignored body")
	}
}

func TestImageSendAdapterSettlement(t *testing.T) {
	for _, mode := range []string{"success", "upload_failed", "lost_response", "read_failed", "wrong_author", "reordered", "extra_text", "missing_image", "wrong_target", "wrong_type", "revoked", "parent"} {
		t.Run(mode, func(t *testing.T) {
			f, a, q := sendFixture(t, "chat")
			input := ImageSendInput{Body: "准确\n\n说明 @**原文**\n", Images: []ImageRef{{12, "img_0123456789abcdef0123456789abcdef"}, {13, "img_fedcba9876543210fedcba9876543210"}}}
			q.Input, _ = json.Marshal(input)
			calls := 0
			source := func(context.Context, ImageSendInput) (PreparedImageMessage, error) {
				return PreparedImageMessage{Body: input.Body, Images: []SendImage{{Data: []byte("first")}, {Data: []byte("second")}}}, nil
			}
			writer := func(ctx context.Context, got platform.TargetActionRequest, m PreparedImageMessage) (string, []string, error) {
				calls++
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 180*time.Second || time.Until(deadline) < 170*time.Second {
					t.Fatal("image action inherited text timeout", deadline)
				}
				if got.IdempotencyKey != q.IdempotencyKey || string(got.Input) != string(q.Input) {
					t.Fatal("request changed")
				}
				if mode == "upload_failed" {
					return "", nil, ErrSendNotSent
				}
				if mode == "lost_response" {
					return "", nil, context.DeadlineExceeded
				}
				keys := []string{"img_first", "img_second"}
				kind, content, _ := imagePayload(m.Body, keys)
				msg := f.messages["om_receipt"]
				msg.Type = kind
				msg.Body.Content = content
				switch mode {
				case "read_failed":
					delete(f.messages, "om_receipt")
					return "om_receipt", keys, nil
				case "wrong_author":
					msg.Sender.ID = "ou_other"
				case "reordered":
					_, msg.Body.Content, _ = imagePayload(m.Body, []string{keys[1], keys[0]})
				case "extra_text":
					_, msg.Body.Content, _ = imagePayload(m.Body+"extra", keys)
				case "missing_image":
					_, msg.Body.Content, _ = imagePayload(m.Body, keys[:1])
				case "wrong_target":
					msg.ChatID = "oc_other"
				case "wrong_type":
					msg.Type = "text"
				case "parent":
					msg.ParentID = "om_parent"
				}
				f.messages[msg.ID] = msg
				return msg.ID, keys, nil
			}
			ready := func(context.Context) error {
				if mode == "revoked" {
					return errors.New("permission revoked")
				}
				return nil
			}
			cap := WithImageActions(AcceptedActionCapabilities(f, nil, nil), f, source, writer, ready).Actions[q.Operation]
			if mode != "revoked" {
				i, err := cap.Inspect(context.Background(), q, a)
				if err != nil || len(i.ContentDigest) != 64 {
					t.Fatal(i, err)
				}
				if calls != 0 {
					t.Fatal("Inspect wrote")
				}
			}
			result, err := cap.Execute(context.Background(), q, a)
			want := "unknown"
			if mode == "success" {
				want = "succeeded"
			}
			if mode == "upload_failed" || mode == "revoked" {
				want = "not_applied"
			}
			if err != nil || result.Status != want || calls > 1 {
				t.Fatal(result, err, calls)
			}
			if mode != "revoked" && mode != "upload_failed" && mode != "lost_response" && !result.ResultID.Valid() {
				t.Fatal("lost known message ID")
			}
		})
	}
}
