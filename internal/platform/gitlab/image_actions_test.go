package gitlab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func commentTestImage(t *testing.T) ActionImage {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(b.Bytes())
	return ActionImage{b.Bytes(), "image/png", hex.EncodeToString(hash[:])}
}
func imageTestInput(body string) ImageActionInput {
	return ImageActionInput{body, []ImageRef{{1, "img_0123456789abcdef0123456789abcdef"}}}
}
func TestImageCommentInputAndMarkdown(t *testing.T) {
	valid := imageTestInput("中文\r\nline")
	for _, input := range []ImageActionInput{valid, imageTestInput(""), {Body: "text"}} {
		raw, _ := json.Marshal(input)
		if _, err := DecodeImageActionInput(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"body":null}`, `{"body":""}`, `{"images":null}`, `{"images":[]}`, `{"body":"x","url":"file"}`, `{"images":[{"message_seq":1,"attachment_id":"/tmp/image"}]}`, `{"images":[{"message_seq":1.1,"attachment_id":"img_0123456789abcdef0123456789abcdef"}]}`, `{"images":[{"message_seq":18446744073709551616,"attachment_id":"img_0123456789abcdef0123456789abcdef"}]}`, `{"images":[{"message_seq":1,"attachment_id":"img_0123456789abcdef0123456789abcdef","extra":true}]}`} {
		if _, err := DecodeImageActionInput([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	duplicate := valid
	duplicate.Images = append(duplicate.Images, duplicate.Images[0])
	raw, _ := json.Marshal(duplicate)
	if _, err := DecodeImageActionInput(raw); err == nil {
		t.Fatal("accepted duplicate")
	}
	for _, body := range []string{"", strings.Repeat("x", 4096), "/src/file.go", "中文\n**说明**", "```\ncode\n```", "<script>\nclosed\n</script>", "![image-1](/uploads/00000000000000000000000000000000/image-1.png)"} {
		if _, err := imageMarkdown(body, placeholderPaths(2)); err != nil {
			t.Fatalf("%q: %v", body, err)
		}
	}
	for _, body := range []string{"```\nunclosed", "<script>\nunclosed", "/close", "\u3000/assign\t@someone\r\n", "```\n/close\n```", "/reopen\r\ntext"} {
		if _, err := imageMarkdown(body, placeholderPaths(1)); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
	if _, err := imageMarkdown(strings.Repeat("x", 4096), []string{"/" + strings.Repeat("p", 4000), "/" + strings.Repeat("q", 4000)}); err == nil {
		t.Fatal("accepted oversized generated body")
	}
}

func TestImageCommentNativeHTTP(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		for _, mode := range []string{"ok", "4096", "upload-second", "bad-path", "read-body", "read-error", "thread", "post-body", "post-502", "post-403", "post-invalid", "dispatch"} {
			t.Run(fmt.Sprintf("%t/%s", discussion, mode), func(t *testing.T) {
				uploads, posts, reads := 0, 0, 0
				var origin, body string
				note := func() string {
					raw, _ := json.Marshal(map[string]any{"id": 99, "body": body, "noteable_id": 9001, "noteable_iid": 42, "noteable_type": "MergeRequest", "author": map[string]any{"id": 13, "username": "bot"}})
					return string(raw)
				}
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer fixture-secret" {
						t.Error("credential")
					}
					if strings.HasSuffix(r.URL.Path, "/uploads") {
						uploads++
						if err := r.ParseMultipartForm(3 << 20); err != nil {
							t.Error(err)
							return
						}
						defer r.MultipartForm.RemoveAll()
						f, h, err := r.FormFile("file")
						if err != nil {
							t.Error(err)
							return
						}
						defer f.Close()
						data, _ := io.ReadAll(f)
						expected := commentTestImage(t)
						if !bytes.Equal(data, expected.Data) || h.Header.Get("Content-Type") != "image/png" || h.Filename != fmt.Sprintf("image-%d.png", uploads) {
							t.Error("multipart mismatch")
						}
						if mode == "upload-second" && uploads == 2 {
							w.WriteHeader(403)
							return
						}
						path := fmt.Sprintf("/uploads/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/image-%d.png", uploads)
						if mode == "bad-path" {
							path = "https://foreign.invalid" + path
						}
						_ = json.NewEncoder(w).Encode(map[string]string{"url": path, "markdown": "/close\nmalicious"})
						return
					}
					if r.Method == "POST" {
						posts++
						if uploads != 2 {
							t.Error("partial post")
						}
						var input map[string]string
						if json.NewDecoder(r.Body).Decode(&input) != nil {
							t.Error("bad body")
						}
						body = input["body"]
						if strings.Contains(body, "malicious") || !strings.Contains(body, "![image-2]") {
							t.Error("wrong markdown", body)
						}
						if mode == "post-502" {
							w.WriteHeader(502)
							return
						}
						if mode == "post-403" {
							w.WriteHeader(403)
							return
						}
						if mode == "post-invalid" {
							io.WriteString(w, `{}`)
							return
						}
						data := note()
						if mode == "post-body" {
							data = strings.Replace(data, `"body":`, `"system":true,"body":`, 1)
						}
						io.WriteString(w, data)
						return
					}
					if strings.HasSuffix(r.URL.Path, "/notes/99") {
						reads++
						if mode == "read-error" {
							w.WriteHeader(503)
							return
						}
						data := note()
						if mode == "read-body" {
							data = strings.Replace(data, "image-1", "image-x", 1)
						}
						io.WriteString(w, data)
						return
					}
					if strings.HasSuffix(r.URL.Path, "/discussions/thread") && posts > 0 {
						data := `{"id":"thread","individual_note":false,"notes":[` + note() + `]}`
						if mode == "thread" {
							data = `{"id":"thread","individual_note":false,"notes":[]}`
						}
						io.WriteString(w, data)
						return
					}
					io.WriteString(w, actionPayload(origin, r.URL.Path))
				}))
				defer server.Close()
				origin = server.URL
				q, a := actionFixture(origin, discussion)
				q.IdempotencyKey = strings.Repeat("a", 32)
				input := imageTestInput("中文\n说明")
				if mode == "4096" {
					input.Body = strings.Repeat("x", 4096)
				}
				input.Images = append(input.Images, ImageRef{2, "img_fedcba9876543210fedcba9876543210"})
				q.Input, _ = json.Marshal(input)
				credential := platform.AccessCredential{Kind: "oauth", Token: platform.NewSecret("fixture-secret")}
				read := NewReadAPI(origin, credential.Token, server.Client().Transport)
				source := func(context.Context, ImageActionInput) (PreparedImageComment, error) {
					img := commentTestImage(t)
					return PreparedImageComment{input.Body, []ActionImage{img, img}}, nil
				}
				write := func(ctx context.Context, actual platform.TargetActionRequest, digest, markdown string) (APIResponse, error) {
					if !bytes.Equal(actual.Input, q.Input) || digest == "" {
						t.Error("lost frozen input")
					}
					if mode == "dispatch" {
						return APIResponse{}, ErrNoteNotSent
					}
					return NewImageNoteAPIWithCredential(origin, credential, server.Client().Transport)(ctx, q.Target.Object.Type, "123", "42", q.Target.Discussion.Value, markdown)
				}
				actions, err := ImageReferenceActions(read, nil, write, NewImageUploadAPIWithCredential(origin, credential, server.Client().Transport), source, func(ctx context.Context) error { return ctx.Err() }, "bot", string(q.Input))
				if err != nil {
					t.Fatal(err)
				}
				cap := actions[q.Operation]
				if !cap.Spec.DirectRoomInstructionOnly || cap.Spec.Retry.MaxAttempts != 1 || cap.Spec.Retry.RetryUnknown {
					t.Fatal(cap.Spec)
				}
				inspection, err := cap.Inspect(context.Background(), q, a)
				if err != nil || inspection.ContentDigest == "" || uploads != 0 || posts != 0 {
					t.Fatal(inspection, err)
				}
				result, err := cap.Execute(context.Background(), q, a)
				want := "succeeded"
				wantPosts := 1
				switch mode {
				case "upload-second", "bad-path", "dispatch":
					want = "not_applied"
					wantPosts = 0
				case "read-body", "read-error", "post-body", "post-502", "post-invalid":
					want = "unknown"
				case "post-403":
					want = "not_applied"
				case "thread":
					if discussion {
						want = "unknown"
					}
				}
				if err != nil || result.Status != want || posts != wantPosts || result.RetryClass != "never" {
					t.Fatal(result, err, uploads, posts)
				}
				if (mode == "read-body" || mode == "read-error" || mode == "post-body" || mode == "thread") && result.ResultID.Value != "99" {
					t.Fatal("lost known note ID", result)
				}
				if want == "succeeded" && reads != 1 {
					t.Fatal("no independent read", reads)
				}
			})
		}
	}
}

func TestImageUploadResponseBoundary(t *testing.T) {
	for _, mode := range []string{"ok", "full", "no-url", "cross-project", "foreign", "mismatch", "filename", "query", "fragment", "encoded", "traversal", "markdown", "json", "oversize", "redirect", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				path := "/uploads/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/image-1.png"
				full := "/-/project/123" + path
				switch mode {
				case "cross-project":
					full = "/-/project/124" + path
				case "foreign":
					full = "https://foreign.invalid" + full
				case "mismatch":
					full = strings.Replace(full, strings.Repeat("a", 32), strings.Repeat("b", 32), 1)
				case "filename":
					path = strings.ReplaceAll(path, "image-1.png", "image-2.png")
				case "query":
					path += "?x=1"
				case "fragment":
					path += "#x"
				case "encoded":
					path = strings.Replace(path, "image-1", "%69mage-1", 1)
				case "traversal":
					path = "/uploads/../" + path
				case "markdown":
					path += ")\n/close"
				case "json":
					io.WriteString(w, "{")
					return
				case "oversize":
					io.WriteString(w, strings.Repeat("x", (64<<10)+1))
					return
				case "redirect":
					w.Header().Set("Location", "/leak")
					w.WriteHeader(307)
					return
				}
				if mode == "no-url" {
					path = ""
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"url": path, "full_path": full})
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			path, err := NewImageUploadAPIWithCredential(server.URL, platform.AccessCredential{Kind: "oauth", Token: platform.NewSecret("fixture-secret")}, server.Client().Transport)(ctx, "123", 1, commentTestImage(t), "g/p")
			good := mode == "ok" || mode == "full" || mode == "no-url"
			if (err == nil) != good || good && path == "" || calls > 1 || mode == "cancelled" && calls != 0 {
				t.Fatal(path, err, calls)
			}
		})
	}
}
