package gitlaboauth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
)

const ServiceSource = "service"

type Application struct {
	registry.OAuthApplication
	Name   string `json:"name"`
	Status string `json:"status"`
	Secret string `json:"secret,omitempty"`
}

func (Application) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED service application]") }

// Snapshot is private control-plane data. Only Public may be returned to users.
type Snapshot struct {
	Version      int           `json:"version"`
	Applications []Application `json:"applications"`
}

func (Snapshot) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED service application catalog]") }

type Store struct {
	OtherGrant func(applicationID string, nativeUserID int64, spaceID string) (bool, error)
	Load       func() (Snapshot, error)
	Save       func(Snapshot) error
}
type Catalog struct {
	mu     sync.Mutex
	store  Store
	saved  Snapshot
	failed bool
	uses   map[*Use]binding
}
type binding struct {
	user, space, operation, state, app string
	revoking                           bool
	cancel                             context.CancelFunc
	done                               chan struct{}
}

func Open(store Store) (*Catalog, error) {
	f, err := store.Load()
	if err != nil {
		return nil, err
	}
	if f.Version != 1 || len(f.Applications) > 100 {
		return nil, registry.ErrInvalid
	}
	f.Applications = slices.Clone(f.Applications)
	seen := map[string]bool{}
	clients := map[string]bool{}
	for i, a := range f.Applications {
		f.Applications[i].Scopes = slices.Clone(a.Scopes)
		if !validApplication(a) || seen[a.ID] || clients[a.Origin+"/"+a.ClientID] {
			return nil, registry.ErrInvalid
		}
		seen[a.ID] = true
		clients[a.Origin+"/"+a.ClientID] = true
	}
	return &Catalog{store: store, saved: f, uses: map[*Use]binding{}}, nil
}
func validApplication(a Application) bool {
	name, origin, err := registry.Normalize(a.Name, a.Origin)
	return err == nil && name == a.Name && origin == a.Origin && opaqueID(a.ID) && a.Source == ServiceSource && a.Version > 0 && a.OwnerID > 0 && platform.ValidCredential(a.ClientID) && a.ClientSecret == "" && platform.ValidCredential(a.Secret) && registry.ValidOAuthRedirect(a.RedirectURI) && registry.GitLabLoginScopes(a.Scopes) && (a.Status == "active" || a.Status == "disabled")
}
func opaqueID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func (c *Catalog) Public() []Application {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Application, 0, len(c.saved.Applications))
	for _, a := range c.saved.Applications {
		a.Secret = ""
		a.Scopes = slices.Clone(a.Scopes)
		out = append(out, a)
	}
	return out
}

// Update preserves public identity on rotation. A different origin, client,
// callback or maximum scope is a new explicit application, never a rebind of
// existing grants. State is durable before old uses are canceled and joined.
func (c *Catalog) Update(ctx context.Context, input Application, expected uint64) (Application, error) {
	c.mu.Lock()
	if c.failed {
		c.mu.Unlock()
		return Application{}, registry.ErrConflict
	}
	index := -1
	for i, a := range c.saved.Applications {
		if a.ID == input.ID {
			index = i
			if a.Version != expected {
				c.mu.Unlock()
				return Application{}, registry.ErrConflict
			}
			if input.Secret == "" {
				input.Secret = a.Secret
			}
			want := a.OAuthApplication
			want.Version = input.Version
			if !Equal(want, input.OAuthApplication) || input.Name != a.Name {
				c.mu.Unlock()
				return Application{}, registry.ErrConflict
			}
			break
		}
	}
	if index < 0 && (expected != 0 || len(c.saved.Applications) >= 100) {
		c.mu.Unlock()
		return Application{}, registry.ErrConflict
	}
	if expected == ^uint64(0) {
		c.mu.Unlock()
		return Application{}, registry.ErrCapacity
	}
	input.Version = expected + 1
	input.Scopes = slices.Clone(input.Scopes)
	if !validApplication(input) {
		c.mu.Unlock()
		return Application{}, registry.ErrRequest
	}
	for _, a := range c.saved.Applications {
		if a.ID != input.ID && a.Origin == input.Origin && a.ClientID == input.ClientID {
			c.mu.Unlock()
			return Application{}, registry.ErrConflict
		}
	}
	next := Snapshot{Version: 1, Applications: slices.Clone(c.saved.Applications)}
	if index < 0 {
		next.Applications = append(next.Applications, input)
	} else {
		next.Applications[index] = input
	}
	err := c.store.Save(next)
	if err == nil {
		c.saved = next
	} else {
		c.failed = true
	}
	var pending []<-chan struct{}
	for _, b := range c.uses {
		if b.app == input.ID || c.failed {
			b.cancel()
			pending = append(pending, b.done)
		}
	}
	c.mu.Unlock()
	if err != nil {
		return Application{}, err
	}
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return Application{}, ctx.Err()
		}
	}
	input.Secret = ""
	input.Scopes = slices.Clone(input.Scopes)
	return input, nil
}
func (c *Catalog) Settle(ctx context.Context, id string) error {
	c.mu.Lock()
	exists := false
	for _, a := range c.saved.Applications {
		if a.ID == id {
			exists = true
		}
	}
	if c.failed || !exists {
		c.mu.Unlock()
		return registry.ErrConflict
	}
	var pending []<-chan struct{}
	for _, b := range c.uses {
		if b.app == id {
			pending = append(pending, b.done)
		}
	}
	c.mu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (c *Catalog) CallbackSpace(state string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed || state == "" {
		return "", false
	}
	for _, b := range c.uses {
		if b.state == state {
			return b.space, true
		}
	}
	return "", false
}
func (c *Catalog) Impact(id string) []map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []map[string]string{}
	for _, b := range c.uses {
		if b.app == id {
			out = append(out, map[string]string{"user_id": b.user, "space_id": b.space, "operation_id": b.operation})
		}
	}
	return out
}
func (c *Catalog) Close(ctx context.Context) error {
	c.mu.Lock()
	c.failed = true
	var pending []<-chan struct{}
	for _, b := range c.uses {
		b.cancel()
		pending = append(pending, b.done)
	}
	c.mu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type scoped struct {
	catalog     *Catalog
	user, space string
}

func (c *Catalog) ForSpace(user, space string) Source { return scoped{c, user, space} }
func (scoped) Managed() bool                          { return true }
func (s scoped) Applications() ([]registry.OAuthApplication, error) {
	c := s.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return nil, registry.ErrConflict
	}
	out := []registry.OAuthApplication{}
	for _, a := range c.saved.Applications {
		if a.Status == "active" {
			v := a.OAuthApplication
			v.Scopes = slices.Clone(v.Scopes)
			out = append(out, v)
		}
	}
	return out, nil
}
func (s scoped) current(app registry.OAuthApplication) (Application, error) {
	if s.catalog.failed || s.user == "" || !opaqueID(s.space) {
		return Application{}, platform.ErrCredential
	}
	for _, a := range s.catalog.saved.Applications {
		if a.Status == "active" && Equal(a.OAuthApplication, app) {
			return a, nil
		}
	}
	return Application{}, platform.ErrCredential
}
func (s scoped) Check(app registry.OAuthApplication) error {
	c := s.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := s.current(app)
	return err
}
func (s scoped) Begin(ctx context.Context, app registry.OAuthApplication, operation, state string) (*Use, error) {
	c := s.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	return s.beginLocked(ctx, app, operation, state)
}
func (s scoped) beginLocked(ctx context.Context, app registry.OAuthApplication, operation, state string) (*Use, error) {
	c := s.catalog
	app.Scopes = slices.Clone(app.Scopes)
	a, err := s.current(app)
	if err != nil {
		return nil, err
	}
	if operation == "" || ctx.Err() != nil {
		return nil, errors.New("OAuth operation unavailable")
	}
	for _, b := range c.uses {
		if b.app == app.ID && b.revoking {
			return nil, registry.ErrConflict
		}
		if state != "" && b.state == state || b.user == s.user && b.space == s.space && b.operation == operation {
			return nil, registry.ErrConflict
		}
	}
	useCtx, cancel := context.WithCancel(ctx)
	b := binding{user: s.user, space: s.space, operation: operation, state: state, app: app.ID, cancel: cancel, done: make(chan struct{})}
	u := &Use{ctx: useCtx, app: app, secret: platform.NewSecret(a.Secret)}
	u.guard = func(write func() error) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, err := s.current(app); err != nil {
			return err
		}
		if err := useCtx.Err(); err != nil {
			return err
		}
		return write()
	}
	var once sync.Once
	u.close = func() { once.Do(func() { cancel(); c.mu.Lock(); delete(c.uses, u); close(b.done); c.mu.Unlock() }) }
	c.uses[u] = b
	return u, nil
}

// Revocation closes admission to the application while it consumes the token.
// An unknown native user, a sharing grant, or another live operation requires
// explicit cleanup instead of an unsafe cross-space remote revocation.
func (s scoped) BeginRevoke(ctx context.Context, app registry.OAuthApplication, operation string, user int64) (*Use, error) {
	c := s.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if user <= 0 || c.store.OtherGrant == nil {
		return nil, registry.ErrConflict
	}
	for _, b := range c.uses {
		if b.app == app.ID {
			return nil, registry.ErrConflict
		}
	}
	other, err := c.store.OtherGrant(app.ID, user, s.space)
	if err != nil || other {
		return nil, registry.ErrConflict
	}
	u, err := s.beginLocked(ctx, app, operation, "")
	if err != nil {
		return nil, err
	}
	b := c.uses[u]
	b.revoking = true
	c.uses[u] = b
	return u, nil
}

func (c *Catalog) RequiresRestart() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.failed }
