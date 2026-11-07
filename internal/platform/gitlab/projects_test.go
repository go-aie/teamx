package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func TestListProjectsRemoteSearchAndPagination(t *testing.T) {
	read := func(_ context.Context, path string, q url.Values, _ http.Header, limit int64) (APIResponse, error) {
		if path != "/projects" || q.Get("search") != "团队/项目 & test" || q.Get("search_namespaces") != "true" || q.Get("page") != "2" || q.Get("per_page") != "50" || q.Get("order_by") != "id" || limit > 1<<20 {
			t.Fatal("unexpected project query", path, q, limit)
		}
		return APIResponse{Data: []byte(`[{"id":7,"name":"项目","path_with_namespace":"team/project","description":"private metadata"}]`), Header: http.Header{"X-Next-Page": {"3"}}}, nil
	}
	page, err := ListProjects(context.Background(), read, "团队/项目 & test", 2)
	if err != nil || len(page.Projects) != 1 || page.Projects[0].Path != "team/project" || page.NextPage != 3 {
		t.Fatal(page, err)
	}
	raw, _ := json.Marshal(page)
	var public map[string]any
	_ = json.Unmarshal(raw, &public)
	if len(public["projects"].([]any)[0].(map[string]any)) != 3 {
		t.Fatal("extra remote metadata disclosed")
	}
}

func TestListProjectsRejectsMalformedPages(t *testing.T) {
	for _, tc := range []struct{ body, next string }{
		{"null", ""}, {`{}`, ""}, {`[{"id":0,"name":"project","path_with_namespace":"team/project"}]`, ""},
		{`[{"id":7,"name":"project","path_with_namespace":"team/../project"}]`, ""},
		{`[]`, "1"}, {`[]`, "https://foreign.invalid/next"},
	} {
		t.Run(tc.body+tc.next, func(t *testing.T) {
			read := func(context.Context, string, url.Values, http.Header, int64) (APIResponse, error) {
				return APIResponse{Data: []byte(tc.body), Header: http.Header{"X-Next-Page": {tc.next}}}, nil
			}
			if _, err := ListProjects(context.Background(), read, "", 1); err == nil {
				t.Fatal("malformed project page accepted")
			}
		})
	}
}
