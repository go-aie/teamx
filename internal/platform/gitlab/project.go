package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

var errProjectVerification = errors.New("GitLab project verification failed")

// VerifyProject checks the exact instance, project path and clone URL. It never
// follows redirects or exposes remote bodies or transport errors.
func VerifyProject(ctx context.Context, origin, project string, secret platform.Secret) (int64, error) {
	return VerifyProjectWithTransport(ctx, origin, project, secret, http.DefaultTransport)
}
func VerifyProjectWithTransport(ctx context.Context, origin, project string, secret platform.Secret, transport http.RoundTripper) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/v4/projects/"+url.PathEscape(project), nil)
	if err != nil {
		return 0, errProjectVerification
	}
	req.Header.Set("PRIVATE-TOKEN", secret.Reveal())
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0, errProjectVerification
	}
	defer resp.Body.Close()
	var result struct {
		ID   int64  `json:"id"`
		Path string `json:"path_with_namespace"`
		URL  string `json:"http_url_to_repo"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode != http.StatusOK || dec.Decode(&result) != nil || dec.Decode(new(any)) != io.EOF || result.ID <= 0 || result.Path != project || result.URL != origin+"/"+project+".git" {
		return 0, errProjectVerification
	}
	return result.ID, nil
}
