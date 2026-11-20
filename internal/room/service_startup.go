//go:build darwin || linux

package room

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Local configuration and rejected endpoint identities cannot become ready by
// polling. Network failures and temporary HTTP responses may still recover.
type serviceStartupError struct{ err error }

func (e *serviceStartupError) Error() string { return e.err.Error() }
func (e *serviceStartupError) Unwrap() error { return e.err }

func serviceStartupFailure(err error) error { return &serviceStartupError{err: err} }

type serviceEndpoint struct {
	config startupConfig
	r      serviceRegistration
	listen string
	token  string
	client *http.Client
}

func newServiceEndpoint(r serviceRegistration) (*serviceEndpoint, error) {
	c, err := readStartupConfig(filepath.Join(r.Home, "config.toml"), true, false)
	if err != nil {
		return nil, serviceStartupFailure(err)
	}
	if c.Access.LoginEnabled != nil && !*c.Access.LoginEnabled {
		return nil, serviceStartupFailure(errors.New("managed deployment requires login enabled"))
	}
	listen := "127.0.0.1:8088"
	if c.Server.Listen != nil {
		listen = *c.Server.Listen
	}
	if err := validateManagedAccess(c, listen); err != nil {
		return nil, serviceStartupFailure(err)
	}
	if strings.HasSuffix(listen, ":0") {
		return nil, serviceStartupFailure(errors.New("service manager requires a fixed loopback port"))
	}
	token, err := readAdminTokenForUID(adminTokenPath(c), uint32(r.UID))
	if err != nil {
		return nil, serviceStartupFailure(err)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &serviceEndpoint{config: c, r: r, listen: listen, token: token, client: client}, nil
}

func (e *serviceEndpoint) close() { e.client.CloseIdleConnections() }

func (e *serviceEndpoint) get(ctx context.Context, route string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, internalAccessEndpoint(e.listen)+route, nil)
	if err != nil {
		return serviceStartupFailure(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	res, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		err := fmt.Errorf("service check %s at %s: HTTP %d", route, e.listen, res.StatusCode)
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			err = fmt.Errorf("监听地址 %s 拒绝当前安装的认证（HTTP %d）；请检查端口是否被其他实例占用，或登录令牌是否匹配", e.listen, res.StatusCode)
		}
		if res.StatusCode < 500 && res.StatusCode != http.StatusRequestTimeout && res.StatusCode != http.StatusTooManyRequests && res.StatusCode != http.StatusTooEarly {
			return serviceStartupFailure(err)
		}
		return err
	}
	if err := decodeServiceJSON(res.Body, result); err != nil {
		return serviceStartupFailure(fmt.Errorf("service check %s at %s: %w", route, e.listen, err))
	}
	return nil
}

func (e *serviceEndpoint) authenticate(ctx context.Context) error {
	var session struct {
		Operator       string `json:"operator"`
		InstallationID string `json:"installation_id"`
	}
	if err := e.get(ctx, "/v1/access/session", &session); err != nil {
		return err
	}
	if session.InstallationID != e.r.InstallationID || session.Operator != e.config.Access.OperatorID {
		return serviceStartupFailure(fmt.Errorf("监听地址 %s 属于其他安装实例或用户", e.listen))
	}
	return nil
}

// A matching authenticated installation may occupy its port before stop. After
// stop, and immediately before start, the configured address must be available.
// Never terminate an arbitrary process merely because it owns this port.
func checkServicePort(ctx context.Context, r serviceRegistration, allowCurrent bool) error {
	endpoint, err := newServiceEndpoint(r)
	if err != nil {
		return err
	}
	defer endpoint.close()
	listener, err := net.Listen("tcp", endpoint.listen)
	if err == nil {
		return listener.Close()
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		return serviceStartupFailure(fmt.Errorf("无法使用监听地址 %s: %w", endpoint.listen, err))
	}
	if allowCurrent && endpoint.authenticate(ctx) == nil {
		return nil
	}
	return serviceStartupFailure(fmt.Errorf("监听地址 %s 已被其他程序或另一 TeamX 实例占用；请先停止对应程序或修改监听地址后重试", endpoint.listen))
}
