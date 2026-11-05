//go:build e2e

package gitlab

import (
	"bytes"
	"context"
	"image"
	"os"
	"strconv"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

// Explicit read-only acceptance against an operator-selected existing upload.
// The image stays in memory; neither the PAT nor response body is logged.
func TestUploadRealDownload(t *testing.T) {
	if os.Getenv("TEAMX_UPLOAD_REAL") != "1" {
		t.Skip("explicit real upload opt-in required")
	}
	project, err := strconv.ParseInt(os.Getenv("TEAMX_UPLOAD_PROJECT"), 10, 64)
	if err != nil {
		t.Fatal("valid TEAMX_UPLOAD_PROJECT required")
	}
	source := platform.SourceRef{Provider: "gitlab", Instance: os.Getenv("TEAMX_UPLOAD_ORIGIN"), ProjectID: project}
	upload, ok := ParseUpload(os.Getenv("TEAMX_UPLOAD_PATH"), source)
	if !ok || os.Getenv("GITLAB_PAT") == "" {
		t.Fatal("upload origin/path and GITLAB_PAT required")
	}
	data, mime, err := DownloadUpload(context.Background(), source.Instance, platform.NewSecret(os.Getenv("GITLAB_PAT")), upload, nil)
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		t.Fatal("invalid downloaded image")
	}
	t.Logf("downloaded image: MIME=%s format=%s bytes=%d dimensions=%dx%d; memory only", mime, format, len(data), config.Width, config.Height)
}
