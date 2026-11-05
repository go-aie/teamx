package gitlab

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func uploadFixture() (platform.SourceRef, string) {
	return platform.SourceRef{Provider: "gitlab", Instance: "https://gitlab.example.com", ProjectID: 312, Project: "group/project"}, "/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png"
}

type countedUploadBody struct {
	io.Reader
	read int
}

func (b *countedUploadBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (*countedUploadBody) Close() error { return nil }

func TestDownloadUploadCallerLimitsStreamAndDecodedDimensions(t *testing.T) {
	source, path := uploadFixture()
	upload, _ := ParseUpload(path, source)
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	largePixels := bytes.Clone(pngBytes.Bytes())
	// A tiny valid header can describe a huge image. DecodeConfig must reject
	// its dimensions before any allocation of the decoded pixel buffer.
	binary.BigEndian.PutUint32(largePixels[16:20], 100000)
	binary.BigEndian.PutUint32(largePixels[20:24], 100000)
	binary.BigEndian.PutUint32(largePixels[29:33], crc32.ChecksumIEEE(largePixels[12:29]))
	for _, tc := range []struct {
		name    string
		data    []byte
		length  int64
		limits  UploadLimits
		want    error
		maxRead int
	}{
		{"valid", pngBytes.Bytes(), -1, UploadLimits{MaxBytes: 1024, MaxPixels: 6}, nil, 1024},
		{"declared-bytes", pngBytes.Bytes(), 1025, UploadLimits{MaxBytes: 1024, MaxPixels: 6}, ErrUploadLimit, 0},
		{"stream-bytes", bytes.Repeat([]byte("x"), 2048), -1, UploadLimits{MaxBytes: 1024, MaxPixels: 6}, ErrUploadLimit, 1025},
		{"pixels", largePixels, -1, UploadLimits{MaxBytes: 1024, MaxPixels: 16 << 20}, ErrUploadLimit, 1024},
		{"malformed", []byte("GIF89a"), -1, UploadLimits{MaxBytes: 1024, MaxPixels: 6}, ErrUploadFormat, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &countedUploadBody{Reader: bytes.NewReader(tc.data)}
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, ContentLength: tc.length, Body: body, Header: make(http.Header)}, nil
			})
			data, _, err := DownloadUploadWithLimits(context.Background(), source.Instance, platform.NewSecret("test-token"), upload, transport, tc.limits)
			if !errors.Is(err, tc.want) || body.read > tc.maxRead || tc.want != nil && len(data) != 0 {
				t.Fatal(err, body.read, len(data))
			}
		})
	}
}

func TestUploadSourceAndNotificationEvidence(t *testing.T) {
	source, path := uploadFixture()
	for _, prefix := range []string{"/uploads/", "/-/project/312/uploads/", "/group/project/-/uploads/", "/group/project/uploads/", source.Instance + "/-/project/312/uploads/"} {
		raw := prefix + "524bb5cf042cd0bb3ef9a7a117a744b7/image.png"
		text := notification.Quote("**检查**\n![image]("+raw+"){width=900 height=536}\n@reviewer", 2048)
		got, ok := FindUpload(text, source, path)
		if !ok || got.ProjectID != 312 || got.Filename != "image.png" {
			t.Fatal(raw, got, ok)
		}
		if restored := NotificationMarkdown(text); !strings.Contains(restored, "**检查**") || !strings.Contains(restored, "![image]("+raw+")") || strings.Contains(restored, "@reviewer") {
			t.Fatal(restored)
		}
	}
	for _, raw := range []string{
		"https://evil.invalid" + path, "//gitlab.example.com" + path,
		"https://user@gitlab.example.com" + path, path + "?token=x", path + "#x",
		strings.Replace(path, "image.png", "../image.png", 1), strings.Replace(path, "image.png", "%2e%2e%2fimage.png", 1),
		strings.Replace(path, "image.png", "%5cimage.png", 1), "/-/project/313" + path,
		"https://gitlab.example.com.evil.invalid" + path, strings.Replace(path, "image.png", "%00.png", 1),
	} {
		if _, ok := ParseUpload(raw, source); ok {
			t.Fatal("untrusted upload accepted", raw)
		}
	}
	if _, ok := FindUpload(notification.Quote("![image]("+path+")", 2048), source, strings.Replace(path, "image.png", "other.png", 1)); ok {
		t.Fatal("accepted attachment absent from notification")
	}
	// Unescape one layer only: the author's literal escaped markup stays literal.
	if got := NotificationMarkdown(notification.Quote(`\*literal\*`, 2048)); got != `> \*literal\*` {
		t.Fatal(got)
	}
	if got := NotificationMarkdown(notification.Quote("```go\n    call()\n```\n\n- parent\n  - child", 2048)); !strings.Contains(got, ">     call()") || !strings.Contains(got, ">   - child") {
		t.Fatal("lost Markdown indentation", got)
	}
}

func TestDownloadUploadBinaryAuthenticationAndMIME(t *testing.T) {
	source, path := uploadFixture()
	upload, _ := ParseUpload(path, source)
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	closed := false
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.String() != source.Instance+"/api/v4/projects/312"+path || r.Header.Get("PRIVATE-TOKEN") != "private-test-token" {
			t.Fatal("wrong attachment request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/octet-stream"}}, Body: closeBody{Reader: bytes.NewReader(imageBytes.Bytes()), closed: &closed}}, nil
	})
	data, mime, err := DownloadUpload(context.Background(), source.Instance, platform.NewSecret("private-test-token"), upload, transport)
	if err != nil || mime != "image/png" || !bytes.Equal(data, imageBytes.Bytes()) || !closed {
		t.Fatal(mime, err, closed)
	}
}

func TestDownloadUploadRejectsRedirectHTMLSVGSizeAndTransportLeaks(t *testing.T) {
	source, path := uploadFixture()
	upload, _ := ParseUpload(path, source)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		length int64
	}{
		{"redirect", 302, "", 0}, {"permission", 403, "private-test-token", 0}, {"missing", 404, "secret-remote-detail", 0},
		{"login", 200, "<!doctype html><html>login</html>", 0}, {"svg", 200, `<svg xmlns="http://www.w3.org/2000/svg"></svg>`, 0},
		{"declared-size", 200, "", MaxUploadBytes + 1}, {"actual-size", 200, strings.Repeat("x", MaxUploadBytes+1), -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, closed := 0, false
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": {"https://evil.invalid"}}, ContentLength: tc.length, Body: closeBody{Reader: strings.NewReader(tc.body), closed: &closed}}, nil
			})
			data, _, err := DownloadUpload(context.Background(), source.Instance, platform.NewSecret("private-test-token"), upload, transport)
			if err == nil || len(data) != 0 || calls != 1 || !closed || strings.Contains(err.Error(), "private-test-token") || strings.Contains(err.Error(), "secret-remote-detail") {
				t.Fatal(err, calls, closed)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("GIF89a"))}, nil
	})
	if data, _, err := DownloadUpload(ctx, source.Instance, platform.NewSecret("private-test-token"), upload, transport); err == nil || len(data) != 0 {
		t.Fatal("returned body after cancellation")
	}
	transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("private-test-token in transport") })
	if _, _, err := DownloadUpload(context.Background(), source.Instance, platform.NewSecret("private-test-token"), upload, transport); err == nil || strings.Contains(err.Error(), "private-test-token") {
		t.Fatal(err)
	}
}
