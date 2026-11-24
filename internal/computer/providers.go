package computer

import (
	"net"
	"net/url"
	"strings"
	"sync"
)

type Factory func(endpoint, credential string) (ManagedProvider, error)
type FactoryForName func(name, endpoint, credential string) (ManagedProvider, error)

var providers = struct {
	sync.RWMutex
	items map[string]Factory
}{items: map[string]Factory{}}

// Register selects compiled implementations, without constructing connections.
func Register(name string, factory Factory) {
	providers.Lock()
	defer providers.Unlock()
	if name == "" || factory == nil || providers.items[name] != nil {
		panic("invalid duplicate Computer provider")
	}
	providers.items[name] = factory
}

func Connect(name, endpoint, credential string) (ManagedProvider, error) {
	providers.RLock()
	factory := providers.items[name]
	providers.RUnlock()
	if factory == nil {
		return nil, Fail(Unavailable, "Computer provider is not available in this build")
	}
	return factory(endpoint, credential)
}

func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || strings.TrimSpace(endpoint) != endpoint || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return Fail(Invalid, "explicit HTTP(S) origin without credentials, query or proxy path required")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return Fail(Invalid, "remote Computer connections require HTTPS")
	}
	return nil
}
