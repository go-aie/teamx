package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func TestDiscussionResolverExtensionGETOnlyAndBounded(t *testing.T) {
	a, _, _ := testApp(t, nil)
	a.transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("not GET", r.Method)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":13,"username":"bot"}`)), Header: http.Header{}}, nil
	})
	out := &bytes.Buffer{}
	a.out = out
	if code := a.run(context.Background(), []string{"notifications", "resolve", "--queries", `[{"source":{"provider":"gitlab","instance":"https://gitlab.example.com","reference":{"version":1}}}]`}); code != 0 {
		t.Fatal(code)
	}
	var rs []gitlab.DiscussionResolution
	if json.Unmarshal(out.Bytes(), &rs) != nil || len(rs) != 1 || rs[0].Status != "mismatch" {
		t.Fatal(out.String())
	}
}
