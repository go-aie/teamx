// Package aio implements the private authenticated Screen Runtime protocol.
// Native MCP connections are never exposed to its callers.
package aio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio/protocol"
)

type Provider struct {
	endpoint, token string
	http            *http.Client
}

func New(endpoint, token string) (*Provider, error) {
	if err := computer.ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	if len(token) < 32 || len(token) > 4096 || strings.TrimSpace(token) != token {
		return nil, computer.Fail(computer.Invalid, "management credential required")
	}
	return &Provider{strings.TrimSuffix(endpoint, "/"), token, &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (p *Provider) request(ctx context.Context, path string, req protocol.Request, out any) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if len(raw) > computer.RequestLimit {
		return computer.Fail(computer.Invalid, "request exceeds 128 KiB")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+p.token)
	r.Header.Set("Content-Type", "application/json")
	response, err := p.http.Do(r)
	if err != nil {
		return computer.Fail(computer.Unavailable, "Runtime transport interrupted; query original operation ID, never replay with a new ID")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, computer.ResponseLimit+65537))
	if err != nil {
		return err
	}
	if len(body) > computer.ResponseLimit+65536 {
		return computer.Fail(computer.Unavailable, "Runtime response too large")
	}
	if response.StatusCode != http.StatusOK {
		var ce computer.Error
		if json.Unmarshal(body, &ce) == nil && ce.Code != "" {
			return &ce
		}
		return computer.Fail(computer.Unavailable, fmt.Sprintf("Runtime HTTP %d", response.StatusCode))
	}
	if out == nil {
		return nil
	}
	if err = json.Unmarshal(body, out); err != nil {
		return computer.Fail(computer.Unavailable, "invalid Runtime response")
	}
	return nil
}
func (p *Provider) Inspect(ctx context.Context) (computer.Info, error) {
	var out computer.Info
	err := p.request(ctx, "/inspect", protocol.Request{}, &out)
	return out, err
}
func (p *Provider) Screens(ctx context.Context, owner computer.Owner) ([]computer.ScreenInfo, error) {
	var out []computer.ScreenInfo
	err := p.request(ctx, "/screens", protocol.Request{Claim: computer.Claim{Owner: owner}}, &out)
	return out, err
}
func (p *Provider) Claim(ctx context.Context, c computer.Claim) (string, error) {
	var out string
	err := p.request(ctx, "/claim", protocol.Request{Claim: c}, &out)
	return out, err
}
func (p *Provider) SetMemberAdmission(ctx context.Context, a computer.MemberAdmission) error {
	return p.request(ctx, "/member/admission", protocol.Request{Admission: a}, nil)
}
func (p *Provider) EnsureScreen(ctx context.Context, b computer.Binding) (computer.Screen, error) {
	var out computer.ScreenInfo
	if err := p.request(ctx, "/ensure", protocol.Request{Binding: b}, &out); err != nil {
		return nil, err
	}
	return &screen{p, out}, nil
}

type screen struct {
	p    *Provider
	info computer.ScreenInfo
}

func (s *screen) Info() computer.ScreenInfo { return s.info }
func (s *screen) Tools(ctx context.Context) (computer.Contract, error) {
	var out computer.Contract
	err := s.p.request(ctx, "/screen/tools", protocol.Request{Screen: s.info}, &out)
	return out, err
}
func (s *screen) Call(ctx context.Context, c computer.Call) (computer.Outcome, error) {
	var out computer.Outcome
	err := s.p.request(ctx, "/screen/call", protocol.Request{Screen: s.info, Call: c}, &out)
	return out, err
}
func (s *screen) Operation(ctx context.Context, id string) (computer.Outcome, error) {
	var out computer.Outcome
	err := s.p.request(ctx, "/screen/operation", protocol.Request{Screen: s.info, OperationID: id}, &out)
	return out, err
}
func (s *screen) Drain(ctx context.Context, scope computer.Scope) error {
	return s.p.request(ctx, "/screen/drain", protocol.Request{Screen: s.info, Scope: scope}, nil)
}
func (s *screen) Stop(ctx context.Context) error {
	return s.p.request(ctx, "/screen/stop", protocol.Request{Screen: s.info}, nil)
}

// Restore binds a saved identity for reconciliation; every remote request still
// validates the current service lease and screen epoch.
func (p *Provider) Restore(info computer.ScreenInfo) computer.Screen { return &screen{p, info} }

var _ computer.Provider = (*Provider)(nil)

func (s *screen) Control(ctx context.Context, req computer.ControlRequest) (computer.ControlState, error) {
	var out computer.ControlState
	err := s.p.request(ctx, "/screen/control", protocol.Request{Screen: s.info, Control: req}, &out)
	return out, err
}
func (s *screen) ViewAsset(ctx context.Context, name string) ([]byte, error) {
	var out []byte
	err := s.p.request(ctx, "/screen/asset", protocol.Request{Screen: s.info, Asset: name}, &out)
	return out, err
}
func (s *screen) View(ctx context.Context, req computer.ViewRequest) (io.ReadWriteCloser, error) {
	raw, err := json.Marshal(protocol.Request{Screen: s.info, View: req})
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, s.p.endpoint+"/screen/view", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+s.p.token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "teamx-rfb")
	client := &http.Client{Transport: viewTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(r)
	if err != nil {
		return nil, computer.Fail(computer.Unavailable, "screen display transport unavailable")
	}
	if res.StatusCode != 101 || res.Header.Get("Upgrade") != "teamx-rfb" {
		res.Body.Close()
		return nil, computer.Fail(computer.HumanControl, "screen display or input lease unavailable")
	}
	stream, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		res.Body.Close()
		return nil, computer.Fail(computer.Unavailable, "screen display upgrade unavailable")
	}
	return stream, nil
}

var viewTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 10 * time.Second
	return t
}()
