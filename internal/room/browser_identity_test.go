package room

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserInstallationIdentityIsStableAndPrivate(t *testing.T) {
	c, token := testAccessConfig(t, t.TempDir())
	first, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.installationID) != 32 || first.installationID != second.installationID {
		t.Fatal("installation identity changed across restart")
	}
	h := first.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, requestInstallationID(r)) }))
	if w := accessRequest(h, "GET", "/v1/ui", "", "", "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := accessRequest(h, "GET", "/healthz", "", "", "", nil); w.Body.String() == first.installationID {
		t.Fatal("public identity leak")
	}
	if w := accessRequest(h, "GET", "/v1/ui", token, "", "", nil); w.Body.String() != first.installationID {
		t.Fatal(w.Code, w.Body.String())
	}
	// A managed release uses its installation registration, not a token or path hash.
	c.installationID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	managed, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil || managed.installationID != c.installationID {
		t.Fatal(err)
	}
	c.installationID = ""
	base := filepath.Dir(c.path)
	if c.Server.DataDir != nil {
		base = *c.Server.DataDir
	}
	if err := os.WriteFile(filepath.Join(base, "installation-id"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newServerAccess(c, "http://127.0.0.1:8088"); err == nil {
		t.Fatal("silently replaced corrupt installation identity")
	}
}
