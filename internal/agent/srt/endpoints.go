package srt

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
)

type endpointRoute struct {
	Host    string `json:"host"`
	Address string `json:"address"`
}

func routedEndpoint(raw string) (string, *endpointRoute, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", nil, errors.New("invalid execution endpoint")
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return raw, nil, nil
	}
	// A DNS spelling avoids the pinned DSH/Go clients' unconditional loopback
	// bypass. Only the host-side SRT resolver knows it, and pins it to this
	// exact original address. No external DNS lookup or DSH patch is involved.
	address := host
	if host == "localhost" {
		address = "127.0.0.1"
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	sum := sha256.Sum256([]byte(u.Scheme + "://" + net.JoinHostPort(address, port)))
	alias := "tx-" + hex.EncodeToString(sum[:20]) + ".execution.invalid"
	u.Host = net.JoinHostPort(alias, port)
	return u.String(), &endpointRoute{Host: alias, Address: address}, nil
}

func (f *Factory) RouteEndpoint(raw string) (string, error) {
	routed, _, err := routedEndpoint(raw)
	return routed, err
}
