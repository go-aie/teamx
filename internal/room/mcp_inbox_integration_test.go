//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/lark"
)

func TestMCPInboxPlatformImagesAndRevokedCache(t *testing.T) {
	for _, provider := range []string{"gitlab", "lark"} {
		t.Run(provider, func(t *testing.T) {
			var svc *service
			var id MemberIdentity
			var data []byte
			var downloads func() int
			var retire func()
			switch provider {
			case "gitlab":
				h, s, _, reader, payload := modelAttachmentFixture(t)
				h.m.bindPlatformReferences(s)
				svc, id, data = s, reader.MemberIdentity, payload
				count := 0
				h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
					count++
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
				})
				downloads = func() int { return count }
				// The same attachment admission used by production rejects changed credentials.
				retire = func() { h.m.mu.Lock(); defer h.m.mu.Unlock(); h.m.closing = true }
			case "lark":
				h, member, _, payload := larkImageHarness(t, false)
				svc, id, data = h.svc, member, payload
				h.m.bindPlatformReferences(svc)
				saveMaterialTurn(t, svc, id, 0, 1)
				downloads = func() int {
					b, _ := os.ReadFile(filepath.Join(h.dir, "downloads"))
					return strings.Count(string(b), "\n")
				}
				retire = func() {
					state := h.state()
					retired, err := lark.Retire(state)
					if err != nil {
						t.Fatal(err)
					}
					h.m.mu.Lock()
					err = h.m.notifications.SaveLocked(retired)
					h.m.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			a, _, _ := accessFixture(t)
			token, _ := issueInboxCredential(t, a, svc, id, true)
			before := downloads()
			result, view := mustInboxCall(t, a, token, 1, "", 25)
			if len(view.Images) != 1 || view.Images[0].Status != "supplied" || len(result.Content) != 3 {
				t.Fatal("no platform image", view, result)
			}
			raw, _ := json.Marshal(result.Content[2])
			var block struct{ Type, Data, MimeType string }
			if json.Unmarshal(raw, &block) != nil {
				t.Fatal("invalid image", string(raw))
			}
			decoded, err := base64.StdEncoding.DecodeString(block.Data)
			if err != nil || !bytes.Equal(decoded, data) || block.Type != "image" || block.MimeType != "image/png" {
				t.Fatal("image content differs")
			}
			if status, err := svc.memberInboxStatus(context.Background(), id); err != nil || status.ConsumedThrough != 1 {
				t.Fatal("no formal consumption", status, err)
			}
			mustInboxCall(t, a, token, 1, "", 25)
			if downloads() != before+1 {
				t.Fatal("cache downloaded again")
			}
			retire()
			if _, err := callInbox(t, a, token, map[string]any{"turn_number": 1}, nil); err == "" {
				t.Fatal("revoked cached attachment was exposed")
			}
		})
	}
}
