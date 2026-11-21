package gitlab

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
)

func imageExtension(mime string) string {
	return map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[mime]
}

// Upload errors describe only authored boundaries, never server bodies/tokens.
type imageUploadError struct{ code, message string }

func (e *imageUploadError) Error() string { return e.message }

// NewImageUploadAPIWithCredential permits only one bounded project upload. No
// redirects, write replay, caller filename, arbitrary endpoint or disk access.
func NewImageUploadAPIWithCredential(origin string, credential platform.AccessCredential, transport http.RoundTripper) ImageUploadAPI {
	client := boundedHTTPClient(transport)
	return func(ctx context.Context, project string, index int, image ActionImage, projectPath string) (string, error) {
		failed := func() (string, error) {
			return "", &imageUploadError{code: "image_upload_invalid", message: "项目图片上传或响应无法核实。"}
		}
		id, err := strconv.ParseInt(project, 10, 64)
		mime, imageErr := attachment.ValidateImage(image.Data, attachment.ImageLimits{MaxBytes: 2 << 20, MaxPixels: 16 << 20})
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != project || !validOrigin(origin) || !validToken(credential.Token.Reveal()) || index < 1 || index > 4 || imageErr != nil || mime != image.MIME || imageExtension(mime) == "" || !actionText(projectPath) || ctx.Err() != nil {
			return failed()
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		filename := fmt.Sprintf("image-%d.%s", index, imageExtension(mime))
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		headers := textproto.MIMEHeader{}
		headers.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
		headers.Set("Content-Type", mime)
		part, err := form.CreatePart(headers)
		if err != nil {
			return failed()
		}
		if _, err = part.Write(image.Data); err != nil {
			return failed()
		}
		if form.Close() != nil {
			return failed()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/v4/projects/"+project+"/uploads", bytes.NewReader(body.Bytes()))
		if err != nil {
			return failed()
		}
		req.GetBody = nil
		if setAuthentication(req, origin, credential) != nil {
			return failed()
		}
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return "", &imageUploadError{code: "image_upload_interrupted", message: "项目图片上传中断；核对原账号连接后再处理。"}
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return "", &imageUploadError{code: fmt.Sprintf("image_upload_http_%d", response.StatusCode), message: fmt.Sprintf("GitLab 项目图片上传 HTTP %d；核对原账号的项目上传权限和连接，不切换账号补发。", response.StatusCode)}
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
		if err != nil || len(raw) > 64<<10 {
			return failed()
		}
		raw, err = safeAPIJSON(raw, credential.Token)
		if err != nil {
			return failed()
		}
		var wire struct {
			URL      string `json:"url"`
			FullPath string `json:"full_path"`
		}
		if decodeJSON(raw, &wire) != nil {
			return failed()
		}
		source := platform.SourceRef{Provider: "gitlab", Instance: origin, ProjectID: id, Project: projectPath}
		var selected Upload
		path := ""
		for _, value := range []string{wire.URL, wire.FullPath} {
			if value == "" {
				continue
			}
			// Reject encoding/Markdown syntax before ParseUpload decodes paths. Only
			// the generated filename and exact project may survive this boundary.
			if strings.ContainsAny(value, "%[]()<>\"'`?#\\\r\n\x00\t ") {
				return failed()
			}
			upload, ok := ParseUpload(value, source)
			if !ok || upload.Filename != filename || path != "" && selected != upload {
				return failed()
			}
			selected = upload
			if path == "" {
				path = value
			}
		}
		if path == "" {
			return failed()
		}
		// A project-relative URL is the native Markdown reference, even when GitLab
		// also returns an absolute full_path. Never trust its returned markdown field.
		if strings.HasPrefix(path, origin+"/") {
			path = strings.TrimPrefix(path, origin)
		}
		return path, nil
	}
}
