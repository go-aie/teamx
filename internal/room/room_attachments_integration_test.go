//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func attachmentQuery(info ServiceInfo, id string) string {
	return url.Values{"room_id": {info.RoomID}, "room_instance": {info.RoomInstance}, "service_epoch": {info.ServiceEpoch}, "caller_id": {humanOperator()}, "attachment_id": {id}}.Encode()
}
func roomAttachmentHTTP(t *testing.T, server *httptest.Server, method, path, contentType string, body io.Reader) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data, response.Header
}
func attachmentJSON(t *testing.T, server *httptest.Server, path string, body any) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	status, data, _ := roomAttachmentHTTP(t, server, "POST", path, "application/json", bytes.NewReader(b))
	return status, data
}
func attachmentUploadHTTP(t *testing.T, server *httptest.Server, info ServiceInfo, id string, data []byte, extra bool) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "截图.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(data); err != nil {
		t.Fatal(err)
	}
	if extra {
		if err = writer.WriteField("extra", "not permitted"); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	status, b, _ := roomAttachmentHTTP(t, server, "POST", "/v1/attachments/upload?"+attachmentQuery(info, id), writer.FormDataContentType(), &body)
	return status, b
}
func TestRoomAttachmentsHTTPLifecycle(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, log := range []bool{false, true} {
			t.Run(fmt.Sprintf("managed=%v/log=%v", managed, log), func(t *testing.T) {
				dir := t.TempDir()
				config := serviceConfig{unifiedInputs: log}
				svc, err := newService(dir, "http-attachments", config)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { closeService(t, svc) }()
				var manager *roomManager
				var handler http.Handler = svc
				if managed {
					manager = testManager(t, t.TempDir(), nil)
					manager.mu.Lock()
					manager.runtimes[svc.info.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
					manager.mu.Unlock()
					handler = manager
				}
				server := httptest.NewServer(handler)
				defer server.Close()
				data := attachmentTestPNG(t, true)
				if len(data) <= 128<<10 {
					t.Fatal("fixture does not exercise upload JSON bypass", len(data))
				}
				id := attachmentTestID(1)
				status, b := attachmentUploadHTTP(t, server, svc.info, id, data, false)
				if status != 200 {
					t.Fatal(status, string(b))
				}
				var upload struct {
					Attachment RoomAttachment `json:"attachment"`
				}
				if err = json.Unmarshal(b, &upload); err != nil {
					t.Fatal(err)
				}
				if upload.Attachment.Size != int64(len(data)) || upload.Attachment.MIMEType != "image/png" {
					t.Fatal(upload)
				}
				original := append([]byte(nil), b...)
				status, b = attachmentUploadHTTP(t, server, svc.info, id, data, false)
				if status != 200 || !bytes.Equal(original, b) {
					t.Fatal("upload retry changed receipt", status, string(b))
				}
				caller := Caller{Author: Author{"human", humanOperator()}}
				states := roomAttachmentStateRequest{ServiceInfo: svc.info, Caller: caller, IDs: []string{id}}
				status, b = attachmentJSON(t, server, "/v1/attachments/state", states)
				if status != 200 || !strings.Contains(string(b), `"status":"available"`) {
					t.Fatal(status, string(b))
				}
				status, b, _ = roomAttachmentHTTP(t, server, "GET", "/v1/attachments/content?"+attachmentQuery(svc.info, id), "", nil)
				if status != 200 || !bytes.Equal(b, data) {
					t.Fatal("draft preview", status)
				}
				status, b = attachmentUploadHTTP(t, server, svc.info, attachmentTestID(2), data, true)
				if status == 200 {
					t.Fatal("multipart extras accepted")
				}
				state, err := svc.store.LoadRoom()
				if err != nil || state.LatestSeq != 0 {
					t.Fatal("draft operations published message", state, err)
				}
				send := apiRequest{ServiceInfo: svc.info, Caller: caller, ClientMessageID: "same-send", Attachments: []RoomAttachmentInput{{id}}}
				status, b = attachmentJSON(t, server, "/v1/send", send)
				if status != 200 {
					t.Fatal(status, string(b))
				}
				var receipt PostReceipt
				if err = json.Unmarshal(b, &receipt); err != nil || receipt.Seq != 1 {
					t.Fatal(string(b), err)
				}
				status, b = attachmentJSON(t, server, "/v1/send", send)
				if status != 200 {
					t.Fatal(status, string(b))
				}
				var retry PostReceipt
				_ = json.Unmarshal(b, &retry)
				if retry != receipt {
					t.Fatal("send retry duplicated", retry)
				}
				content := "/v1/attachments/content?" + attachmentQuery(svc.info, id) + "&seq=1"
				var headers http.Header
				status, b, headers = roomAttachmentHTTP(t, server, "GET", content, "", nil)
				if status != 200 || !bytes.Equal(b, data) || headers.Get("Content-Type") != "image/png" || headers.Get("X-Content-Type-Options") != "nosniff" || headers.Get("Cache-Control") != "no-store" {
					t.Fatal(status, headers)
				}
				wrong := send
				wrong.Caller.ID = "other"
				wrong.ClientMessageID = "forbidden"
				status, b = attachmentJSON(t, server, "/v1/send", wrong)
				if status != 403 {
					t.Fatal("operator mismatch", status, string(b))
				}
				state, err = svc.store.LoadRoom()
				if err != nil || state.LatestSeq != 1 {
					t.Fatal(state, err)
				}
				// Restart the actual backend and update only its service binding. Object and
				// message identities remain unchanged, including the original send key.
				before := svc.info
				closeService(t, svc)
				svc, err = newService(dir, "http-attachments", config)
				if err != nil {
					t.Fatal(err)
				}
				server.Close()
				if managed {
					manager.mu.Lock()
					manager.runtimes[svc.info.RoomIdentity].service = svc
					manager.mu.Unlock()
					handler = manager
				} else {
					handler = svc
				}
				server = httptest.NewServer(handler)
				defer server.Close()
				status, b, _ = roomAttachmentHTTP(t, server, "GET", "/v1/attachments/content?"+attachmentQuery(before, id)+"&seq=1", "", nil)
				if status != 403 {
					t.Fatal("old epoch accepted", status, string(b))
				}
				send.ServiceInfo = svc.info
				status, b = attachmentJSON(t, server, "/v1/send", send)
				if status != 200 {
					t.Fatal(status, string(b))
				}
				_ = json.Unmarshal(b, &retry)
				if retry != receipt {
					t.Fatal("restart duplicate", retry, receipt)
				}
				status, b, _ = roomAttachmentHTTP(t, server, "GET", "/v1/attachments/content?"+attachmentQuery(svc.info, id)+"&seq=1", "", nil)
				if status != 200 || !bytes.Equal(b, data) {
					t.Fatal("restart preview", status)
				}
				// A different Room has the same operator but cannot resolve this object.
				other, err := newService(t.TempDir(), "other-room", config)
				if err != nil {
					t.Fatal(err)
				}
				defer closeService(t, other)
				if _, release, resolveErr := other.store.prepareRoomAttachments(caller.ID, send.Attachments); resolveErr == nil {
					if release != nil {
						release()
					}
					t.Fatal("cross-Room reference accepted")
				}
				var snapshot uint64 = 1
				status, b = attachmentJSON(t, server, "/v1/messages/clear", apiRequest{ServiceInfo: svc.info, Caller: caller, SnapshotSeq: &snapshot})
				if status != 200 {
					t.Fatal(status, string(b))
				}
				status, b, _ = roomAttachmentHTTP(t, server, "GET", "/v1/attachments/content?"+attachmentQuery(svc.info, id)+"&seq=1", "", nil)
				if status != 403 {
					t.Fatal("cleared message still authorizes content", status, string(b))
				}
			})
		}
	}
}
func TestRoomAttachmentsHTTPRejectsForgedDescriptorsAndLifecycle(t *testing.T) {
	svc, err := newService(t.TempDir(), "http-attachments", serviceConfig{imageMaterials: imageMaterialConfig{MaxImages: 1, ImageBytes: 1024, TotalBytes: 1024, MaxPixels: 100}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	server := httptest.NewServer(svc)
	defer server.Close()
	caller := Caller{Author: Author{"human", humanOperator()}}
	status, b, _ := roomAttachmentHTTP(t, server, "GET", "/v1/info", "", nil)
	if status != 200 || !strings.Contains(string(b), `"max_images":1`) || !strings.Contains(string(b), `"image_bytes":1024`) {
		t.Fatal(status, string(b))
	}
	raw := fmt.Sprintf(`{"room_id":%q,"room_instance":%q,"service_epoch":%q,"caller":{"kind":"human","id":%q},"client_message_id":"forged","attachments":[{"id":%q,"mime_type":"image/png"}]}`, svc.info.RoomID, svc.info.RoomInstance, svc.info.ServiceEpoch, caller.ID, attachmentTestID(1))
	status, b, _ = roomAttachmentHTTP(t, server, "POST", "/v1/send", "application/json", strings.NewReader(raw))
	if status != 400 {
		t.Fatal("trusted metadata accepted from client", status, string(b))
	}
	svc.restarting.Store(true)
	status, b = attachmentUploadHTTP(t, server, svc.info, attachmentTestID(1), []byte("invalid"), false)
	if status != 409 {
		t.Fatal("restart admitted upload", status, string(b))
	}
	svc.restarting.Store(false)
	svc.beginClosing()
	status, b = attachmentUploadHTTP(t, server, svc.info, attachmentTestID(1), []byte("invalid"), false)
	if status != 503 {
		t.Fatal("closing admitted upload", status, string(b))
	}
	if _, err := os.Stat(svc.store.attachmentDir()); !os.IsNotExist(err) {
		t.Fatal("lifecycle rejection created files", err)
	}
}

func TestRoomAttachmentsHTTPDraftExpiryAndStoredPreview(t *testing.T) {
	svc, err := newService(t.TempDir(), "draft-state", serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	server := httptest.NewServer(svc)
	defer server.Close()
	data := attachmentTestPNG(t, false)
	status, b := attachmentUploadHTTP(t, server, svc.info, attachmentTestID(1), data, false)
	if status != 200 {
		t.Fatal(status, string(b))
	}
	status, b = attachmentUploadHTTP(t, server, svc.info, attachmentTestID(2), data, false)
	if status != 200 {
		t.Fatal(status, string(b))
	}
	caller := Caller{Author: Author{"human", humanOperator()}}
	send := apiRequest{ServiceInfo: svc.info, Caller: caller, ClientMessageID: "retained", Attachments: []RoomAttachmentInput{{attachmentTestID(1)}}}
	status, b = attachmentJSON(t, server, "/v1/send", send)
	if status != 200 {
		t.Fatal(status, string(b))
	}
	svc.store.mu.Lock()
	m, err := svc.store.attachmentMeta(attachmentTestID(1))
	svc.store.attachmentStorage().now = func() time.Time { return m.CreatedAt.Add(25 * time.Hour) }
	svc.store.attachmentStorage().limits.ImageBytes = 1
	svc.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	status, b = attachmentJSON(t, server, "/v1/attachments/state", roomAttachmentStateRequest{ServiceInfo: svc.info, Caller: caller, IDs: []string{attachmentTestID(1), attachmentTestID(2), attachmentTestID(3)}})
	if status != 200 || !strings.Contains(string(b), `"status":"available"`) || !strings.Contains(string(b), `"status":"expired"`) || !strings.Contains(string(b), `"status":"missing"`) {
		t.Fatal(status, string(b))
	}
	status, b, _ = roomAttachmentHTTP(t, server, "GET", "/v1/attachments/content?"+attachmentQuery(svc.info, attachmentTestID(1))+"&seq=1", "", nil)
	if status != 200 || !bytes.Equal(b, data) {
		t.Fatal("lowering limits broke stored preview", status)
	}
	status, b = attachmentJSON(t, server, "/v1/send", send)
	if status != 200 {
		t.Fatal("lowering limits broke committed retry", status, string(b))
	}
	send.ClientMessageID = "new-reference"
	status, b = attachmentJSON(t, server, "/v1/send", send)
	if status != 400 {
		t.Fatal("new send ignored lower limits", status, string(b))
	}
}
