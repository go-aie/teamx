package room

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func restartAccess(t *testing.T, a *serverAccess) (*serverAccess, http.Handler) {
	t.Helper()
	c := freshAccessConfig(t, filepath.Dir(a.config.path))
	fresh, err := newServerAccess(c, a.internal)
	if err != nil {
		t.Fatal(err)
	}
	fresh.document = newSystemSettings(c).configDocument
	return fresh, fresh.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
}

func TestAccessSessionsSurviveRestartAndLogoutOnlyCurrentDevice(t *testing.T) {
	a, token, h := accessFixture(t)
	desktop := accessLogin(t, h, token)
	phone := accessLogin(t, h, token)
	if desktop.MaxAge != 30*24*60*60 || !desktop.HttpOnly {
		t.Fatal("unexpected persistent cookie", desktop)
	}
	data, err := os.ReadFile(a.sessionPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{token, desktop.Value, phone.Value} {
		if strings.Contains(string(data), secret) {
			t.Fatal("session file contains a raw credential")
		}
	}
	info, err := os.Stat(a.sessionPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("session file is not private", err)
	}
	fresh, handler := restartAccess(t, a)
	for _, cookie := range []*http.Cookie{desktop, phone} {
		if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", cookie); w.Code != 200 {
			t.Fatal("restart lost login", w.Code)
		}
	}
	if w := accessRequest(handler, "POST", "/v1/access/logout", "", fresh.internal, "{}", desktop); w.Code != 200 {
		t.Fatal("logout failed", w.Code)
	}
	_, handler = restartAccess(t, fresh)
	if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", desktop); w.Code != 401 {
		t.Fatal("restart resurrected logged-out device", w.Code)
	}
	if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", phone); w.Code != 200 {
		t.Fatal("logout affected another device", w.Code)
	}
}

func TestAccessSessionsExpireWithoutRenewingOnRestart(t *testing.T) {
	a, token, h := accessFixture(t)
	issued := time.Now().Add(-29 * 24 * time.Hour)
	a.now = func() time.Time { return issued }
	cookie := accessLogin(t, h, token)
	fresh, handler := restartAccess(t, a)
	fresh.now = func() time.Time { return issued.Add(accessSessionLifetime - time.Second) }
	if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", cookie); w.Code != 200 {
		t.Fatal("session expired early", w.Code)
	}
	fresh.now = func() time.Time { return issued.Add(accessSessionLifetime) }
	if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", cookie); w.Code != 401 {
		t.Fatal("restart extended session lifetime", w.Code)
	}
	// Loading an already expired snapshot prunes it durably too.
	a, token, h = accessFixture(t)
	a.now = func() time.Time { return time.Now().Add(-accessSessionLifetime - time.Hour) }
	cookie = accessLogin(t, h, token)
	fresh, handler = restartAccess(t, a)
	if len(fresh.sessions) != 0 {
		t.Fatal("restored expired sessions")
	}
	if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", cookie); w.Code != 401 {
		t.Fatal("restored an expired cookie", w.Code)
	}
}

func TestAccessSessionsEvictionPersistsAcrossRestart(t *testing.T) {
	a, token, h := accessFixture(t)
	now := time.Now()
	a.now = func() time.Time { return now }
	oldest := accessLogin(t, h, token)
	var newest *http.Cookie
	for range maxAccessSessions {
		now = now.Add(time.Second)
		newest = accessLogin(t, h, token)
	}
	fresh, handler := restartAccess(t, a)
	if len(fresh.sessions) != maxAccessSessions {
		t.Fatal("session limit lost on restart", len(fresh.sessions))
	}
	if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", oldest); w.Code != 401 {
		t.Fatal("evicted session restored", w.Code)
	}
	if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", newest); w.Code != 200 {
		t.Fatal("newest session lost", w.Code)
	}
}

func TestAccessSessionsPasswordReplacementCannotResurrectOldCookies(t *testing.T) {
	a, token, h := accessFixture(t)
	cookie := accessLogin(t, h, token)
	for _, password := range []string{"replacement-strong-access-token", token} {
		if err := os.WriteFile(adminTokenPath(a.config), []byte(password+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		fresh, handler := restartAccess(t, a)
		if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", cookie); w.Code != 401 {
			t.Fatal("password replacement restored old cookie", w.Code)
		}
		a = fresh
	}
}

func TestAccessSettingsPersistSessionRevocation(t *testing.T) {
	for _, change := range []string{"save", "password", "disable"} {
		t.Run(change, func(t *testing.T) {
			a, token, h := accessFixture(t)
			a.document = newSystemSettings(a.config).configDocument
			desktop, phone := accessLogin(t, h, token), accessLogin(t, h, token)
			replacement := ""
			if change == "password" {
				replacement = "replacement-strong-access-token"
			}
			w := accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, change != "disable", false, replacement, token, ""), desktop)
			if w.Code != 200 {
				t.Fatal("save settings", w.Code, w.Body.String())
			}
			fresh, handler := restartAccess(t, a)
			if change == "disable" {
				// Re-enable the same password and ensure disabling login has
				// permanently discarded both devices' sessions.
				w = accessRequest(handler, "POST", "/v1/access/settings", "", fresh.internal, settingsBody(fresh, true, false, token, "", ""), nil)
				if w.Code != 200 {
					t.Fatal("enable login", w.Code, w.Body.String())
				}
				_, handler = restartAccess(t, fresh)
			}
			for i, cookie := range []*http.Cookie{desktop, phone} {
				want := 401
				if change == "save" && i == 1 {
					want = 200
				}
				if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", cookie); w.Code != want {
					t.Fatal("incorrect revocation after restart", i, w.Code, want)
				}
			}
		})
	}
}

func TestAccessSessionWriteFailuresDoNotAcknowledgeLoginOrLogout(t *testing.T) {
	a, token, h := accessFixture(t)
	cookie := accessLogin(t, h, token)
	a.sessionStore.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("disk unavailable") }
	w := accessRequest(h, "POST", "/v1/access/login", "", a.internal, `{"token":"`+token+`"}`, nil)
	if w.Code != 500 || len(w.Result().Cookies()) != 0 || len(a.sessions) != 1 {
		t.Fatal("failed persistence acknowledged login", w.Code)
	}
	if w = accessRequest(h, "POST", "/v1/access/logout", "", a.internal, "{}", cookie); w.Code != 500 || len(w.Result().Cookies()) != 0 {
		t.Fatal("failed persistence acknowledged logout", w.Code)
	}
	a.document = newSystemSettings(a.config).configDocument
	w = accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, true, false, "replacement-strong-access-token", token, ""), cookie)
	if w.Code != 500 {
		t.Fatal("failed revocation acknowledged password change", w.Code)
	}
	if w = accessRequest(h, "GET", "/v1/ui", token, "", "", nil); w.Code != 200 {
		t.Fatal("failed revocation changed current password", w.Code)
	}
	// A retry after storage recovers must actually revoke the saved session.
	a.sessionStore.ops = defaultSnapshotOps()
	if w = accessRequest(h, "POST", "/v1/access/logout", "", a.internal, "{}", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	_, handler := restartAccess(t, a)
	if w = accessRequest(handler, "GET", "/v1/ui", "", "", "", cookie); w.Code != 401 {
		t.Fatal("logout retry did not persist", w.Code)
	}
}

func TestAccessSessionsRejectUnsafeOrCorruptFiles(t *testing.T) {
	for _, kind := range []string{"permissions", "symlink", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			a, token, h := accessFixture(t)
			accessLogin(t, h, token)
			var err error
			switch kind {
			case "permissions":
				err = os.Chmod(a.sessionPath(), 0644)
			case "symlink":
				err = os.Rename(a.sessionPath(), a.sessionPath()+".original")
				if err == nil {
					err = os.Symlink(a.sessionPath()+".original", a.sessionPath())
				}
			case "corrupt":
				err = os.WriteFile(a.sessionPath(), []byte("{truncated"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := newServerAccess(a.config, a.internal); err == nil {
				t.Fatal("unsafe session file accepted")
			}
		})
	}
}

func TestAccessSessionsAreBoundToInstallationAndOwner(t *testing.T) {
	for _, identity := range []string{"installation", "owner"} {
		t.Run(identity, func(t *testing.T) {
			a, token, h := accessFixture(t)
			cookie := accessLogin(t, h, token)
			c := a.config
			if identity == "installation" {
				c.installationID = strings.Repeat("a", 32)
			} else {
				c.Access.OperatorID = "owner_" + strings.Repeat("b", 32)
			}
			if _, err := newServerAccess(c, a.internal); err == nil {
				t.Fatal("registered installation or owner changed without repair")
			}
			_, handler := restartAccess(t, a)
			if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", cookie); w.Code != 200 {
				t.Fatal("rejected identity change modified the original login", w.Code)
			}
		})
	}
}
