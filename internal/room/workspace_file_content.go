package room

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform/attachment"
)

func workspaceFileLimits() map[string]any {
	return map[string]any{
		"text_bytes": workspaceFileTextBytes, "path_bytes": workspaceFilePathBytes,
		"scan_entries": workspaceFileScanLimit, "list_entries": workspaceFileEntryLimit,
		"image_bytes": workspaceFileImageBytes, "image_pixels": workspaceFileImagePixels,
		"auto_images": workspaceFileAutoImages, "download_bytes": workspaceFileDownloadBytes,
		"read_concurrency": workspaceFileConcurrency, "download_seconds": int(workspaceFileDownloadTimeout / time.Second),
		"preview_kinds": []string{"markdown", "text", "image"}, "download": true,
	}
}

func (f *workspaceFileRoot) regular(p string) (*os.File, os.FileInfo, error) {
	file, err := f.open(p)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 {
		file.Close()
		return nil, nil, fileError("file_not_allowed")
	}
	return file, info, nil
}

func readWorkspaceFile(ctx context.Context, file *os.File, info os.FileInfo, limit int64) ([]byte, error) {
	if info.Size() > limit {
		return nil, fileError("file_too_large")
	}
	b, err := io.ReadAll(io.LimitReader(&contextFileReader{ctx: ctx, reader: file}, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != info.Size() {
		return nil, fileError("file_changed")
	}
	return b, ctx.Err()
}

// Decode with the attachment decoder budget, without creating attachment state.
func workspaceImage(ctx context.Context, b []byte) (string, image.Config, error) {
	select {
	case roomAttachmentDecoders <- struct{}{}:
		defer func() { <-roomAttachmentDecoders }()
	case <-ctx.Done():
		return "", image.Config{}, ctx.Err()
	}
	mimeType, err := attachment.ValidateImage(b, attachment.ImageLimits{MaxBytes: workspaceFileImageBytes, MaxPixels: workspaceFileImagePixels})
	if errors.Is(err, attachment.ErrLimit) {
		return "", image.Config{}, fileError("image_limit_exceeded")
	}
	if err != nil || !slices.Contains([]string{"image/png", "image/jpeg", "image/webp"}, mimeType) {
		return "", image.Config{}, fileError("preview_type_unavailable")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", config, fileError("preview_type_unavailable")
	}
	decoded, _, err := image.Decode(bytes.NewReader(b))
	if err != nil || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return "", config, fileError("preview_type_unavailable")
	}
	return mimeType, config, ctx.Err()
}

func workspaceDownloadOnly(p string) bool {
	return slices.Contains([]string{".svg", ".html", ".htm", ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".zip", ".gz", ".tar"}, strings.ToLower(path.Ext(p)))
}

func (f *workspaceFileRoot) preview(ctx context.Context, p string) (workspaceFilePreview, error) {
	result := workspaceFilePreview{Path: p, Name: path.Base(p), CurrentContent: true, PreviewKind: "unsupported", Reason: "preview_type_unavailable"}
	file, info, err := f.regular(p)
	if err != nil {
		return result, err
	}
	defer file.Close()
	result.Size, result.Mtime = info.Size(), info.ModTime()
	result.DownloadAvailable = info.Size() <= workspaceFileDownloadBytes
	header := make([]byte, 512)
	n, err := file.ReadAt(header, 0)
	if err != nil && err != io.EOF {
		return result, err
	}
	result.MIME = http.DetectContentType(header[:n])
	imageType := strings.HasPrefix(result.MIME, "image/")
	ext := strings.ToLower(path.Ext(p))
	imageExtension := slices.Contains([]string{".png", ".jpg", ".jpeg", ".webp"}, ext)
	switch {
	case workspaceDownloadOnly(p):
	case imageType || imageExtension:
		b, readErr := readWorkspaceFile(ctx, file, info, workspaceFileImageBytes)
		if readErr != nil {
			if !errors.Is(readErr, fileError("file_too_large")) {
				return result, readErr
			}
			result.Reason = "image_limit_exceeded"
		} else {
			mimeType, config, imageErr := workspaceImage(ctx, b)
			if imageErr == nil {
				result.PreviewKind, result.Reason, result.MIME = "image", "", mimeType
				result.Width, result.Height = config.Width, config.Height
			} else if ctx.Err() != nil {
				return result, ctx.Err()
			} else {
				result.Reason = imageErr.Error()
			}
		}
	case info.Size() > workspaceFileTextBytes:
		result.Reason = "file_too_large"
	case result.MIME == "text/html; charset=utf-8":
	default:
		b, err := readWorkspaceFile(ctx, file, info, workspaceFileTextBytes)
		if err != nil {
			return result, err
		}
		if utf8.Valid(b) && !bytes.ContainsRune(b, 0) {
			result.PreviewKind, result.Reason, result.Text = "text", "", string(b)
			if ext == ".md" || ext == ".markdown" {
				result.PreviewKind = "markdown"
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, f.unchanged(p, info)
}

type workspaceFileContent struct {
	file *os.File
	info os.FileInfo
	data []byte // Only validated images are buffered; downloads use the pinned file.
	mime string
}

func (f *workspaceFileRoot) content(ctx context.Context, p string, download bool) (*workspaceFileContent, error) {
	file, info, err := f.regular(p)
	if err != nil {
		return nil, err
	}
	content := &workspaceFileContent{file: file, info: info, mime: "application/octet-stream"}
	if info.Size() > workspaceFileDownloadBytes {
		err = fileError("file_too_large")
	} else if !download {
		if workspaceDownloadOnly(p) {
			err = fileError("file_not_allowed")
		} else {
			content.data, err = readWorkspaceFile(ctx, file, info, workspaceFileImageBytes)
			if err == nil {
				content.mime, _, err = workspaceImage(ctx, content.data)
			}
		}
	}
	if err == nil {
		err = f.unchanged(p, info)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return content, nil
}

// The caller has just revalidated the source and original message. All bytes
// now come from this admitted handle; no pathname is resolved during streaming.
func (c *workspaceFileContent) serve(ctx context.Context, w http.ResponseWriter, name string, download bool) error {
	rc := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if err := rc.SetWriteDeadline(deadline); err != nil {
		return err
	}
	done, stop := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			_ = rc.SetWriteDeadline(time.Now())
		case <-stop:
		}
	}()
	defer func() { close(stop); <-done }()
	w.Header().Set("Content-Type", c.mime)
	disposition := "inline"
	if download {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.FormatInt(c.info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	var reader io.Reader = c.file
	if !download {
		reader = bytes.NewReader(c.data)
	}
	// Avoid sendfile and cap the original size even if a writer appends mid-transfer.
	_, _ = io.CopyN(w, &contextFileReader{ctx: ctx, reader: reader}, c.info.Size())
	return nil // Headers are committed: a transfer failure must not append JSON.
}
