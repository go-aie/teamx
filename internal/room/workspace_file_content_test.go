package room

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceFileImagesValidateRealFormatAndBudgets(t *testing.T) {
	f, dir := fileRootFixture(t)
	png := attachmentTestPNG(t, false)
	webp, err := base64.StdEncoding.DecodeString("UklGRhwAAABXRUJQVlA4TA8AAAAvAkAAAAcQ/Y/+ByKi/wEA")
	if err != nil {
		t.Fatal(err)
	}
	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil); err != nil {
		t.Fatal(err)
	}
	huge := bytes.Clone(png)
	binary.BigEndian.PutUint32(huge[16:20], 6000)
	binary.BigEndian.PutUint32(huge[20:24], 5000)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	for _, tc := range []struct {
		name         string
		data         []byte
		kind, reason string
	}{
		{"valid.png", png, "image", ""}, {"valid.jpg", jpegBytes.Bytes(), "image", ""},
		{"valid.webp", webp, "image", ""},
		{"actual-image.bin", png, "image", ""}, {"fake.png", []byte("plain text"), "unsupported", "preview_type_unavailable"},
		{"broken.png", png[:len(png)/2], "unsupported", "preview_type_unavailable"},
		{"huge.png", huge, "unsupported", "image_limit_exceeded"},
		{"oversize.png", make([]byte, workspaceFileImageBytes+1), "unsupported", "image_limit_exceeded"},
		{"download.html", png, "unsupported", "preview_type_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFileFixture(t, filepath.Join(dir, tc.name), tc.data)
			p, err := f.preview(t.Context(), tc.name)
			if err != nil || p.PreviewKind != tc.kind || p.Reason != tc.reason || !p.DownloadAvailable {
				t.Fatal(p, err)
			}
			content, err := f.content(t.Context(), tc.name, false)
			if tc.kind == "image" {
				if err != nil || !bytes.Equal(content.data, tc.data) || p.Width < 1 || p.Height < 1 {
					t.Fatal(p, err)
				}
				content.file.Close()
			} else if err == nil {
				content.file.Close()
				t.Fatal("unvalidated image delivered")
			}
		})
	}
	// Waiting for a decoder obeys cancellation and does not borrow attachment state.
	for i := 0; i < cap(roomAttachmentDecoders); i++ {
		roomAttachmentDecoders <- struct{}{}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = workspaceImage(ctx, png)
	for i := 0; i < cap(roomAttachmentDecoders); i++ {
		<-roomAttachmentDecoders
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestWorkspaceFileDownloadLimitsAndPinnedHandle(t *testing.T) {
	f, dir := fileRootFixture(t)
	writeFileFixture(t, filepath.Join(dir, "data.bin"), []byte("ORIGINAL"))
	c, err := f.content(t.Context(), "data.bin", true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.file.Close()
	writeFileFixture(t, filepath.Join(dir, "new.bin"), []byte("REPLACEMENT"))
	if err := os.Rename(filepath.Join(dir, "new.bin"), filepath.Join(dir, "data.bin")); err != nil {
		t.Fatal(err)
	}
	if err := f.unchanged("data.bin", c.info); err == nil {
		t.Fatal("replacement admitted")
	}
	b, err := io.ReadAll(c.file)
	if err != nil || string(b) != "ORIGINAL" {
		t.Fatal(string(b), err)
	}
	big, err := os.Create(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(workspaceFileDownloadBytes + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()
	if _, err := f.content(t.Context(), "big.bin", true); !errors.Is(err, fileError("file_too_large")) {
		t.Fatal(err)
	}
	p, err := f.preview(t.Context(), "big.bin")
	if err != nil || p.DownloadAvailable || p.Text != "" {
		t.Fatal(p, err)
	}
}

type workspaceHeaderWriter struct {
	http.ResponseWriter
	admitted func()
}

func (w workspaceHeaderWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w workspaceHeaderWriter) WriteHeader(status int) {
	if status == 200 && w.admitted != nil {
		w.admitted()
	}
	w.ResponseWriter.WriteHeader(status)
}

func TestWorkspaceFileContentHTTPAdmissionAndHeaders(t *testing.T) {
	t.Setenv("USER", humanCaller.ID)
	m, external := directoryTestManager(t)
	room := managerCreate(t, m, "content-files")
	saved, state := readManagedRoom(t, m, room)
	member := saved.Members[0]
	dir, _ := filepath.EvalSymlinks(filepath.Join(m.roomDir(room.RoomID), member.Workspace))
	name := "中文\" file.html"
	data := []byte("<script>throw new Error('never execute')</script>")
	writeFileFixture(t, filepath.Join(dir, name), data)
	writeFileFixture(t, filepath.Join(dir, "image.png"), attachmentTestPNG(t, false))
	rootID := workspaceRootID(room.RoomIdentity, member.MembershipID, dir)
	state.Messages = []RoomMessage{{Seq: 1, ClientMessageID: "delivery", Author: Author{"agent", member.AgentID}, AuthorMembershipID: member.MembershipID, WorkspaceRootID: rootID, Text: "[file](result.md)", CreatedAt: time.Now().UTC()}}
	state.LatestSeq = 1
	state.Revision++
	store, err := OpenStore(m.roomDir(room.RoomID), room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "message_seq": {"1"}, "path": {name}, "download": {"true"}}
	endpoint := func() string { return "/v1/workspace/files/content?" + q.Encode() }
	var onAdmit func()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeHTTP(workspaceHeaderWriter{w, func() {
			if onAdmit != nil {
				onAdmit()
			}
		}}, r)
	}))
	defer server.Close()
	get := func(want int) []byte {
		t.Helper()
		resp, err := server.Client().Get(server.URL + endpoint())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatal(resp.StatusCode, string(b), err)
		}
		if resp.Header.Get("Cache-Control") != "private, no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal(resp.Header)
		}
		if want == 200 && q.Get("download") == "true" {
			disp, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
			if err != nil || disp != "attachment" || params["filename"] != name || resp.Header.Get("Content-Type") != "application/octet-stream" {
				t.Fatal(resp.Header, err)
			}
		}
		return b
	}
	if got := get(200); !bytes.Equal(got, data) {
		t.Fatal(string(got))
	}
	// Prepare a handle, then change the current binding before delivery.
	q.Del("download")
	q.Set("path", "image.png")
	req := httptest.NewRequest("GET", endpoint(), nil)
	snapshot, err := m.workspaceFilesSnapshot(req)
	if err != nil {
		t.Fatal(err)
	}
	source, root, err := snapshot.source(member)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	content, err := root.content(t.Context(), "image.png", false)
	if err != nil {
		t.Fatal(err)
	}
	defer content.file.Close()
	configPath := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
	saved.Workspace.MemberDirectories = map[string]string{member.MembershipID: external}
	if err := m.store.save(configPath, saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := m.admitWorkspaceFile(req, source, root, "image.png", content.info); !errors.Is(err, fileError("workspace_source_changed")) {
		t.Fatal(err)
	}
	get(409)
	saved.Workspace.MemberDirectories = nil
	if err := m.store.save(configPath, saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	png := get(200)
	if !bytes.Equal(png, attachmentTestPNG(t, false)) {
		t.Fatal("image bytes changed")
	}
	// Once delivery starts, clearing and replacing the pathname cannot redirect it.
	q.Set("path", name)
	q.Set("download", "true")
	onAdmit = func() {
		state.ClearedThrough = 1
		state.Messages = []RoomMessage{}
		state.Revision++
		for i := range state.Members {
			state.Members[i].ReadThrough = 1
		}
		if err := store.SaveRoom(state); err != nil {
			panic(err)
		}
		if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, "old")); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("NEW"), 0600); err != nil {
			panic(err)
		}
	}
	if got := get(200); !bytes.Equal(got, data) {
		t.Fatal(string(got))
	}
	onAdmit = nil
	if got := get(404); !strings.Contains(string(got), "reference_unavailable") {
		t.Fatal(string(got))
	}
	if err := m.admitWorkspaceFile(req, source, root, "image.png", content.info); !errors.Is(err, fileError("reference_unavailable")) {
		t.Fatal(err)
	}
	q.Del("message_seq")
	q.Set("membership_id", member.MembershipID)
	q.Set("root_id", rootID)
	if got := get(200); string(got) != "NEW" {
		t.Fatal(string(got))
	}
	q.Del("download")
	q.Set("path", "image.png")
	resp, err := server.Client().Get(server.URL + "/v1/workspace/files/preview?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var preview struct {
		ContentURL string         `json:"content_url"`
		Limits     map[string]any `json:"limits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil || preview.ContentURL == "" || preview.Limits["download_bytes"] != float64(workspaceFileDownloadBytes) {
		t.Fatal(preview, err)
	}

	q.Set("download", "true")
	for _, path := range []string{".teamx/auth/secret", ".git/config", "../secret", "images/../../secret"} {
		q.Set("path", path)
		want := 403
		if strings.Contains(path, "..") {
			want = 400
		}
		get(want)
	}
	q.Set("path", "too-big.bin")
	big, err := os.Create(filepath.Join(dir, "too-big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(workspaceFileDownloadBytes + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()
	get(413)
	q.Set("path", "image.png")
	q.Set("message_seq", "1")
	get(400)
	q.Del("message_seq")
	q.Del("download")
	// Service shutdown rejects all new transfers without starting a runtime.
	m.mu.Lock()
	m.closing = true
	m.mu.Unlock()
	get(503)
	if len(m.runtimes) != 0 {
		t.Fatal("file reading activated a runtime")
	}
}

func TestWorkspaceFileTransferCancellationDeadlineAndShutdown(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			f, dir := fileRootFixture(t)
			writeFileFixture(t, filepath.Join(dir, "data"), bytes.Repeat([]byte("x"), 2<<20))
			finished := make(chan struct{})
			var server *http.Server
			client, endpoint := toolPipeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				// Exercise the same global admission queue as normal content requests.
				workspaceFileReads <- struct{}{}
				defer func() { <-workspaceFileReads }()
				ctx, cancel := context.WithTimeout(r.Context(), 150*time.Millisecond)
				defer cancel()
				content, err := f.content(ctx, "data", true)
				if err != nil {
					t.Error(err)
					return
				}
				defer content.file.Close()
				if err := content.serve(ctx, w, "data", true); err != nil {
					t.Error(err)
				}
			}), func(s *http.Server) { server = s; s.WriteTimeout = time.Millisecond })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			switch mode {
			case "cancel":
				cancel()
			case "shutdown":
				server.Close()
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("blocked transfer retained slot")
			}
			if len(workspaceFileReads) != 0 {
				t.Fatal("transfer leaked admission slot")
			}
		})
	}
}
