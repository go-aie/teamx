package gitlab

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	_ "golang.org/x/image/webp"
)

const MaxUploadBytes = 20 << 20

// Upload is a project attachment, never an arbitrary remote URL.
type Upload struct {
	ProjectID int64
	Secret    string
	Filename  string
}

var uploadSecret = regexp.MustCompile(`^[a-f0-9]{32}$`)
var quotedEscape = regexp.MustCompile("\\\\([\\\\*_\\[\\]<>`#\\-+!|.:()~])")
var uploadCandidates = regexp.MustCompile(`https?://[^\s<>\[\](){}"']+|/[^\s<>\[\](){}"']+`)

// NotificationMarkdown reverses just the escaping added by notification.Quote.
// It is for display/attachment discovery only; saved notification text stays literal.
func NotificationMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "> ") {
			lines[i] = "> " + quotedEscape.ReplaceAllString(line[2:], "$1")
		}
	}
	return strings.Join(lines, "\n")
}

func ParseUpload(raw string, source platform.SourceRef) (Upload, bool) {
	var result Upload
	if source.Provider != "gitlab" || source.ProjectID <= 0 || !validOrigin(source.Instance) || len(raw) > 4096 || strings.ContainsAny(raw, "\\\x00\r\n\t ") {
		return result, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return result, false
	}
	if u.IsAbs() {
		if u.Scheme+"://"+u.Host != source.Instance {
			return result, false
		}
	} else if u.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return result, false
	}
	path := u.Path
	prefixes := []string{"/uploads/", "/-/project/" + strconv.FormatInt(source.ProjectID, 10) + "/uploads/"}
	if source.Project != "" {
		prefixes = append(prefixes, "/"+source.Project+"/-/uploads/", "/"+source.Project+"/uploads/")
	}
	for _, prefix := range prefixes {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
		if len(parts) != 2 || !uploadSecret.MatchString(parts[0]) || parts[1] == "" || parts[1] == "." || parts[1] == ".." || len(parts[1]) > 255 || strings.ContainsAny(parts[1], "\\\x00\r\n") {
			return result, false
		}
		return Upload{ProjectID: source.ProjectID, Secret: parts[0], Filename: parts[1]}, true
	}
	return result, false
}

// FindUpload requires the exact attachment to appear in the saved notification.
// Code/plain-text references are also valid evidence of an attachment's origin.
func FindUpload(text string, source platform.SourceRef, raw string) (Upload, bool) {
	upload, ok := ParseUpload(raw, source)
	if !ok {
		return Upload{}, false
	}
	for _, candidate := range uploadCandidates.FindAllString(NotificationMarkdown(text), -1) {
		if found, valid := ParseUpload(candidate, source); valid && found == upload {
			return upload, true
		}
	}
	return Upload{}, false
}

// DownloadUpload uses the authenticated API, never browser cookies or redirects.
// The response is bounded in memory and validated independently of its MIME header
// (GitLab can return application/octet-stream for a PNG).
func DownloadUpload(ctx context.Context, origin string, secret platform.Secret, upload Upload, transport http.RoundTripper) ([]byte, string, error) {
	return DownloadUploadWithLimits(ctx, origin, secret, upload, transport, UploadLimits{})
}

// UploadLimits may tighten the page's byte cap. MaxPixels enables dimension
// validation for model inputs; zero preserves the page's existing behavior.
type UploadLimits struct {
	MaxBytes  int64
	MaxPixels int64
}

var (
	ErrUploadUnavailable = errors.New("GitLab attachment unavailable")
	ErrUploadLimit       = errors.New("GitLab attachment exceeds image limit")
	ErrUploadFormat      = errors.New("GitLab attachment image format invalid")
)

func DownloadUploadWithLimits(ctx context.Context, origin string, secret platform.Secret, upload Upload, transport http.RoundTripper, limits UploadLimits) ([]byte, string, error) {
	failed := ErrUploadUnavailable
	if limits.MaxBytes == 0 {
		limits.MaxBytes = MaxUploadBytes
	}
	if limits.MaxBytes < 1 || limits.MaxBytes > MaxUploadBytes || limits.MaxPixels < 0 {
		return nil, "", ErrUploadLimit
	}
	if !validOrigin(origin) || !validToken(secret.Reveal()) || upload.ProjectID <= 0 || !uploadSecret.MatchString(upload.Secret) || upload.Filename == "" || upload.Filename == "." || upload.Filename == ".." || strings.ContainsAny(upload.Filename, "/\\\x00\r\n") {
		return nil, "", failed
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	target := origin + "/api/v4/projects/" + strconv.FormatInt(upload.ProjectID, 10) + "/uploads/" + upload.Secret + "/" + url.PathEscape(upload.Filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", failed
	}
	req.Header.Set("PRIVATE-TOKEN", secret.Reveal())
	req.Header.Set("Accept", "image/png, image/jpeg, image/gif, image/webp, application/octet-stream")
	resp, err := boundedHTTPClient(transport).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", failed
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", failed
	}
	if resp.ContentLength > limits.MaxBytes {
		return nil, "", ErrUploadLimit
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limits.MaxBytes+1))
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if err != nil {
		return nil, "", failed
	}
	if int64(len(data)) > limits.MaxBytes {
		return nil, "", ErrUploadLimit
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		if limits.MaxPixels > 0 {
			config, format, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || "image/"+format != mime || config.Width <= 0 || config.Height <= 0 {
				return nil, "", ErrUploadFormat
			}
			if int64(config.Width) > limits.MaxPixels/int64(config.Height) {
				return nil, "", ErrUploadLimit
			}
		}
		return data, mime, nil
	default:
		return nil, "", ErrUploadFormat
	}
}
