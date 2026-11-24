package srt

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestLoopbackTransportIsStableAndExact(t *testing.T) {
	f, r := policyFixture(t)
	endpoint := "http://127.0.0.1:8088/v1/spaces/a/mcp"
	f.config.Endpoints = append(f.config.Endpoints, endpoint)
	routed, err := f.RouteEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(routed)
	if !strings.HasSuffix(u.Hostname(), ".execution.invalid") || u.Port() != "8088" || u.Path != "/v1/spaces/a/mcp" {
		t.Fatal(routed)
	}
	if again, err := f.RouteEndpoint(routed); err != nil || again != routed {
		t.Fatal("routing is not idempotent", again, err)
	}
	r.Endpoints = []string{routed}
	p, err := f.policy(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.routes) != 1 || p.routes[0].Address != "127.0.0.1" || p.routes[0].Host != u.Hostname() || !slices.Contains(p.Network.AllowedDomains, "127.0.0.1:8088") {
		t.Fatal("lost exact original destination", p)
	}
	r.Endpoints = []string{"http://127.0.0.1:8089"}
	if _, err := f.policy(r); err == nil {
		t.Fatal("another host port admitted")
	}
}
