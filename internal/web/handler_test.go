package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"regexp"
	"strings"
	"testing"
)

func TestAssets(t *testing.T) {
	err := fs.WalkDir(files, "assets", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		asset := "/ui/" + strings.TrimPrefix(name, "assets/")
		var contentType string
		switch path.Ext(name) {
		case ".html":
			asset, contentType = "/", "text/html"
			if name == "assets/access.html" {
				asset = "/access"
			}
		case ".css":
			contentType = "text/css"
		case ".js", ".mjs":
			contentType = "text/javascript"
		case ".svg":
			contentType = "image/svg+xml"
		default:
			t.Fatalf("unexpected embedded asset: %s", name)
		}
		response := httptest.NewRecorder()
		if !Serve(response, httptest.NewRequest(http.MethodGet, asset, nil)) ||
			response.Code != http.StatusOK || response.Body.Len() == 0 ||
			!strings.Contains(response.Header().Get("Content-Type"), contentType) || response.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("asset %s: %d, %v", asset, response.Code, response.Header())
		}
		want, err := files.ReadFile(name)
		if err != nil || response.Body.String() != string(want) {
			t.Fatalf("asset %s does not match embedded source: %v", asset, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The browser loads the entry point, its imports and CSS separately. Check the
// real embedded graph so a missing nested route cannot hide behind DOM mocks.
func TestWebAssetReferences(t *testing.T) {
	refs := regexp.MustCompile(`(?m)(?:\bfrom\s*|@import\s+url\(|(?:src|href)=)["']([^"']+)["']`)
	err := fs.WalkDir(files, "assets", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.Contains(name, "/vendor/") {
			return err
		}
		data, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		base, _ := url.Parse("http://teamx.local/ui/" + strings.TrimPrefix(name, "assets/"))
		for _, match := range refs.FindAllSubmatch(data, -1) {
			ref, err := url.Parse(string(match[1]))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(ref.Path, "/ui/") && !strings.HasPrefix(ref.Path, "./") && !strings.HasPrefix(ref.Path, "../") {
				continue
			}
			asset := base.ResolveReference(ref).Path
			response := httptest.NewRecorder()
			if !Serve(response, httptest.NewRequest(http.MethodGet, asset, nil)) || response.Code != http.StatusOK {
				t.Fatalf("%s references unavailable asset %s", name, asset)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range []string{"/ui/missing.mjs", "/ui/features", "/ui/../handler.go", "/ui/vendor/marked-LICENSE.md", "/ui/index.html"} {
		if Serve(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, asset, nil)) {
			t.Fatalf("unexpected public asset: %s", asset)
		}
	}
}
