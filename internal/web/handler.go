package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Serve writes a static UI response and reports whether the path was handled.
// API routes, unknown assets, and non-public files are left to the caller.
func Serve(w http.ResponseWriter, r *http.Request) bool {
	file, contentType := "", ""
	switch r.URL.Path {
	case "/access":
		file, contentType = "assets/access.html", "text/html; charset=utf-8"
	case "/":
		file, contentType = "assets/index.html", "text/html; charset=utf-8"
	default:
		if !strings.HasPrefix(r.URL.Path, "/ui/") {
			return false
		}
		name := strings.TrimPrefix(r.URL.Path, "/ui/")
		if !fs.ValidPath(name) {
			return false
		}
		switch path.Ext(name) {
		case ".css":
			contentType = "text/css; charset=utf-8"
		case ".js", ".mjs":
			contentType = "text/javascript; charset=utf-8"
		case ".svg":
			contentType = "image/svg+xml"
		default:
			return false
		}
		file = "assets/" + name
	}
	data, err := files.ReadFile(file)
	if err != nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "GET or HEAD required", http.StatusMethodNotAllowed)
		return true
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	http.ServeContent(w, r, file, time.Time{}, bytes.NewReader(data))
	return true
}
