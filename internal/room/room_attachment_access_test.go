package room

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAccessAttachmentUploadStreamsHumanBody(t *testing.T) {
	a, admin, _ := accessFixture(t)
	body := "--multipart-boundary\r\n" + strings.Repeat("image bytes", 200000)
	calls := 0
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != body {
			t.Error("access middleware changed multipart bytes", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	cookie := accessLogin(t, h, admin)
	path := "/v1/attachments/upload?caller_id=" + url.QueryEscape(a.space.OwnerUserID)
	for _, login := range []string{"admin", "cookie", "disabled"} {
		t.Run(login, func(t *testing.T) {
			token, session := admin, cookie
			if login == "admin" {
				session = nil
			}
			if login == "cookie" {
				token = ""
			}
			if login == "disabled" {
				a.disabled = true
				token = ""
				session = nil
				defer func() { a.disabled = false }()
			}
			w := accessRequest(h, "POST", path, token, a.internal, body, session)
			if w.Code != http.StatusNoContent {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	for _, query := range []string{"", "?caller_id=forged", "?caller_id=", "?caller_id=" + url.QueryEscape(a.space.OwnerUserID) + "&caller_id=" + url.QueryEscape(a.space.OwnerUserID)} {
		if w := accessRequest(h, "POST", "/v1/attachments/upload"+query, admin, a.internal, body, nil); w.Code != 403 {
			t.Fatal(query, w.Code)
		}
	}
	for _, origin := range []string{"", "https://evil.invalid"} {
		if w := accessRequest(h, "POST", path, "", origin, body, cookie); w.Code != 403 {
			t.Fatal(origin, w.Code)
		}
	}
	if w := accessRequest(h, "POST", path, "", a.internal, body, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	agent, revoke, err := a.issueExecution(executionBinding{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, ServiceEpoch: "epoch"}, Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	if w := accessRequest(h, "POST", path, agent, a.internal, body, nil); w.Code != 403 {
		t.Fatal("agent upload accepted", w.Code)
	}
	if calls != 3 {
		t.Fatal("rejected request reached upload handler", calls)
	}
}
