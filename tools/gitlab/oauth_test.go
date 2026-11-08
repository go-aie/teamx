package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOAuthExtensionAuthenticationAndNoWriteReplay(t *testing.T) {
	calls := 0
	a, _, _ := testApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret-test-token" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("OAuth header mismatch")
		}
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	c, err := a.api()
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.request(context.Background(), "POST", "/api/v4/projects/7/issues/1/notes", map[string]string{"body": "fixture"}, new(any))
	if err == nil || calls != 1 {
		t.Fatal("write replayed", calls, err)
	}
}
