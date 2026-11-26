package room

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

const maxHostedSessions = 256
const maxUserSessions = 4
const maxHostedSessionBytes = 16 << 20

var errSessionStorage = errors.New("session_storage_unavailable")
var errLoginRequired = errors.New("login_required")
var errAuthenticationTemporary = errors.New("authentication_temporarily_unavailable")
var errSessionCapacity = errors.New("session_capacity_reached")

func (c externalAuthConfig) sessionMaxAge() (time.Duration, error) {
	if c.SessionMaxAge == "" {
		return 7 * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(c.SessionMaxAge)
	if err != nil || d <= 0 || d > 30*24*time.Hour {
		return 0, errors.New("session_max_age must be a positive duration of at most 720h")
	}
	return d, nil
}

type renewableSession struct {
	Created       time.Time   `json:"created_at"`
	VerifiedAt    time.Time   `json:"verified_at"`
	VerifiedUntil time.Time   `json:"verified_until"`
	Revision      uint64      `json:"revision"`
	Grant         authn.Grant `json:"grant"`
}

type savedRenewableSession struct {
	userSubject
	Expires time.Time         `json:"absolute_expires_at"`
	Renewal *renewableSession `json:"authentication"`
}

type renewableSnapshot struct {
	Version  int                              `json:"version"`
	Binding  string                           `json:"binding"`
	Sessions map[string]savedRenewableSession `json:"sessions"`
}

type renewalFlight struct {
	done chan struct{}
	err  error
}
type renewalRetry struct {
	failures int
	after    time.Time
	err      error
}

// One owner per host. commit serializes every collection mutation. a.mu guards
// only reads, publication and revocation fences; no IAM, disk or waits under it.
type accessSessionRenewal struct {
	a              *serverAccess
	provider       authn.RenewableProvider
	commit         sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	limit          chan struct{}
	flights        map[[32]byte]*renewalFlight
	retries        map[[32]byte]renewalRetry
	denied         map[[32]byte]bool
	closed, broken bool
	quarantined    map[[32]byte]bool
	maxAge         time.Duration
}

func newAccessSessionRenewal(ctx context.Context, a *serverAccess, provider authn.RenewableProvider) (*accessSessionRenewal, error) {
	age, err := a.config.Deployment.Authentication.sessionMaxAge()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	return &accessSessionRenewal{a: a, provider: provider, ctx: ctx, cancel: cancel, maxAge: age,
		limit: make(chan struct{}, 8), flights: map[[32]byte]*renewalFlight{}, retries: map[[32]byte]renewalRetry{}, denied: map[[32]byte]bool{}, quarantined: map[[32]byte]bool{}}, nil
}

func (o *accessSessionRenewal) valid(s accessSession, now time.Time) bool {
	if s.renewal == nil || !now.Before(s.expires) || o.a.authorizeSubject(s.Subject) != nil {
		return false
	}
	u, ok := o.a.users.user(s.Subject.UserID)
	g := s.renewal.Grant
	return ok && externalIdentityKey(u.Identity) == [3]string{g.Provider, g.Authority, g.Subject} &&
		g.ClientID == o.a.config.Deployment.Authentication.ClientID
}

func (o *accessSessionRenewal) restore() error {
	a := o.a
	data, info, err := readSettingsFileLimit(a.sessionPath(), maxHostedSessionBytes)
	if err != nil || info == nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0600 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("browser sessions must belong to the deployment account with mode 0600")
	}
	var header struct {
		Version int    `json:"version"`
		Binding string `json:"binding"`
	}
	if json.Unmarshal(data, &header) != nil {
		return errors.New("invalid browser sessions file")
	}
	binding, err := hex.DecodeString(header.Binding)
	if err != nil || len(binding) != 32 || hex.EncodeToString(binding) != header.Binding {
		return errors.New("invalid browser session binding")
	}
	next := map[[32]byte]accessSession{}
	if header.Version == 2 {
		// Validate the complete legacy shape before atomically discarding it.
		var legacy accessSessionSnapshot
		if len(data) > maxAccessSessionBytes || json.Unmarshal(data, &legacy) != nil || len(legacy.Sessions) > maxAccessSessions {
			return errors.New("invalid legacy browser sessions")
		}
		for key, s := range legacy.Sessions {
			b, err := hex.DecodeString(key)
			if err != nil || len(b) != 32 || !validOperatorID(s.UserID) || s.AuthVersion == 0 || s.Expires.IsZero() {
				return errors.New("invalid legacy browser session")
			}
		}
	} else if header.Version == 3 {
		var saved renewableSnapshot
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if dec.Decode(&saved) != nil || len(saved.Sessions) > maxHostedSessions {
			return errors.New("invalid browser sessions file")
		}
		counts := map[string]int{}
		for key, entry := range saved.Sessions {
			b, err := hex.DecodeString(key)
			r := entry.Renewal
			if err != nil || len(b) != 32 || hex.EncodeToString(b) != key || r == nil || !validOperatorID(entry.UserID) || entry.AuthVersion == 0 || r.Revision == 0 ||
				r.Created.IsZero() || r.Created.After(r.VerifiedAt) || r.VerifiedAt.After(r.VerifiedUntil) || r.Created.After(a.now()) || !r.Created.Before(entry.Expires) || entry.Expires.After(r.Created.Add(30*24*time.Hour)) ||
				r.VerifiedUntil.After(r.VerifiedAt.Add(authn.IdentityVerificationWindow)) || r.VerifiedUntil.After(r.Grant.AccessExpires) || !validStoredGrant(r.Grant) {
				return errors.New("invalid browser session record")
			}
			counts[entry.UserID]++
			if counts[entry.UserID] > maxUserSessions {
				return errors.New("invalid per-user session count")
			}
			if header.Binding == a.sessionBinding() {
				user, ok := a.users.user(entry.UserID)
				if !ok || externalIdentityKey(user.Identity) != [3]string{r.Grant.Provider, r.Grant.Authority, r.Grant.Subject} || r.Grant.ClientID != a.config.Deployment.Authentication.ClientID {
					return errors.New("invalid browser session identity binding")
				}
			}
			s := accessSession{Subject: entry.userSubject, expires: entry.Expires, renewal: r}
			if shortened := r.Created.Add(o.maxAge); shortened.Before(s.expires) {
				s.expires = shortened
			}
			if header.Binding == a.sessionBinding() && o.valid(s, a.now()) {
				next[[32]byte(b)] = s
			}
		}
	} else {
		return errors.New("invalid browser sessions version")
	}
	return o.change(func(sessions map[[32]byte]accessSession) error {
		clear(sessions)
		maps.Copy(sessions, next)
		return nil
	})
}

func validStoredGrant(g authn.Grant) bool {
	return g.Provider != "" && len(g.Provider) <= 256 && g.Authority != "" && len(g.Authority) <= 4096 && g.Subject != "" && len(g.Subject) <= 256 &&
		g.ClientID != "" && len(g.ClientID) <= 256 && g.AccessToken != "" && len(g.AccessToken) <= 64<<10 && g.RefreshToken != "" && len(g.RefreshToken) <= 64<<10 &&
		!g.AccessExpires.IsZero() && len(g.Nonce) <= 256 && len(g.Scope) <= 4096 && !strings.ContainsAny(g.AccessToken+g.RefreshToken+g.Subject, "\x00\r\n")
}

// The mutation runs against the latest collection. Network callers carry only
// a session revision, never an old map. Revocation may fence memory during save;
// publication preserves every fence and logout subsequently commits its prune.
func (o *accessSessionRenewal) change(mutate func(map[[32]byte]accessSession) error) error {
	o.commit.Lock()
	defer o.commit.Unlock()
	a := o.a
	a.mu.Lock()
	if o.broken || o.closed || o.ctx.Err() != nil {
		a.mu.Unlock()
		return errSessionStorage
	}
	next := maps.Clone(a.sessions)
	for key, s := range next {
		if o.denied[key] || !o.valid(s, a.now()) {
			delete(next, key)
		}
	}
	err := mutate(next)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	saved := renewableSnapshot{Version: 3, Binding: a.sessionBinding(), Sessions: map[string]savedRenewableSession{}}
	for key, s := range next {
		saved.Sessions[hex.EncodeToString(key[:])] = savedRenewableSession{s.Subject, s.expires, s.renewal}
	}
	data, err := json.Marshal(saved)
	if err != nil || len(data)+1 > maxHostedSessionBytes || len(next) > maxHostedSessions {
		return errSessionCapacity
	}
	if err = a.sessionStore.save(a.sessionPath(), saved, maxHostedSessionBytes); err != nil {
		a.mu.Lock()
		if errors.Is(err, ErrCommitUnknown) {
			o.broken = true
			a.revokeUIUpdatesLocked(true)
		}
		a.mu.Unlock()
		return errSessionStorage
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if o.closed || o.broken || o.ctx.Err() != nil {
		return errSessionStorage
	}
	for key, s := range next {
		if o.denied[key] || !o.valid(s, a.now()) {
			delete(next, key)
		}
	}
	a.sessions = next
	for key := range o.denied {
		if _, exists := next[key]; !exists && o.flights[key] == nil && !o.quarantined[key] {
			delete(o.denied, key)
		}
	}
	a.revokeUIUpdatesLocked(false)
	return nil
}

func (o *accessSessionRenewal) fence(key [32]byte) {
	a := o.a
	a.mu.Lock()
	defer a.mu.Unlock()
	o.denied[key] = true
	delete(a.sessions, key)
	delete(o.retries, key)
	a.revokeUIUpdatesLocked(false)
}

func (o *accessSessionRenewal) revoke(key [32]byte) error {
	o.fence(key)
	err := o.change(func(next map[[32]byte]accessSession) error { delete(next, key); return nil })
	if err == nil {
		o.a.mu.Lock()
		delete(o.denied, key)
		delete(o.quarantined, key)
		o.a.mu.Unlock()
	}
	return err
}

func (o *accessSessionRenewal) install(login authn.Login, subject userSubject, old [32]byte) (string, error) {
	token, err := randomAccessToken()
	if err != nil {
		return "", errSessionStorage
	}
	now := o.a.now()
	if !validStoredGrant(login.Grant) || login.VerifiedAt.After(now) || login.VerifiedAt.IsZero() || !now.Before(login.VerifiedUntil) ||
		login.VerifiedUntil.After(login.VerifiedAt.Add(authn.IdentityVerificationWindow)) || login.VerifiedUntil.After(login.Grant.AccessExpires) || externalIdentityKey(login.Identity) != [3]string{login.Grant.Provider, login.Grant.Authority, login.Grant.Subject} {
		return "", errLoginRequired
	}
	s := accessSession{Subject: subject, expires: login.VerifiedAt.Add(o.maxAge), renewal: &renewableSession{Created: login.VerifiedAt, VerifiedAt: login.VerifiedAt, VerifiedUntil: login.VerifiedUntil, Revision: 1, Grant: login.Grant}}
	err = o.change(func(next map[[32]byte]accessSession) error {
		if !o.valid(s, now) {
			return errLoginRequired
		}
		if previous, ok := next[old]; ok && previous.Subject.UserID == subject.UserID {
			delete(next, old)
		}
		count := 0
		var oldest [32]byte
		var created time.Time
		for key, entry := range next {
			if entry.Subject.UserID == subject.UserID {
				count++
				if created.IsZero() || entry.renewal.Created.Before(created) {
					oldest, created = key, entry.renewal.Created
				}
			}
		}
		if count >= maxUserSessions {
			delete(next, oldest)
		}
		if len(next) >= maxHostedSessions {
			return errSessionCapacity
		}
		next[tokenDigest(token)] = s
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func renewAfter(r *renewableSession) time.Time {
	lead := r.VerifiedUntil.Sub(r.VerifiedAt) / 4
	if lead > time.Minute {
		lead = time.Minute
	}
	return r.VerifiedUntil.Add(-lead)
}

// authenticate never reads a business body. A canceled waiter cannot cancel a
// shared refresh, and cannot continue into its handler after the wait completes.
func (o *accessSessionRenewal) authenticate(ctx context.Context, key [32]byte, waitEarly ...bool) (accessSession, error) {
	a := o.a
	a.mu.Lock()
	s, ok := a.sessions[key]
	if o.quarantined[key] {
		a.mu.Unlock()
		return accessSession{}, errSessionStorage
	}
	if o.broken || o.closed || o.ctx.Err() != nil {
		a.mu.Unlock()
		return accessSession{}, errSessionStorage
	}
	if !ok || o.denied[key] || !o.valid(s, a.now()) {
		a.mu.Unlock()
		if ok {
			if err := o.revoke(key); err != nil {
				return accessSession{}, err
			}
		}
		return accessSession{}, errLoginRequired
	}
	if ctx.Err() != nil {
		a.mu.Unlock()
		return accessSession{}, ctx.Err()
	}
	now := a.now()
	if now.Before(renewAfter(s.renewal)) {
		a.mu.Unlock()
		return s, nil
	}
	flight := o.flights[key]
	if flight == nil {
		if retry := o.retries[key]; now.Before(retry.after) {
			a.mu.Unlock()
			if now.Before(s.renewal.VerifiedUntil) && !errors.Is(retry.err, errSessionStorage) {
				return s, nil
			}
			return accessSession{}, retry.err
		}
		flight = &renewalFlight{done: make(chan struct{})}
		o.flights[key] = flight
		o.wg.Add(1)
		go o.run(key, s, flight)
	}
	if now.Before(s.renewal.VerifiedUntil) && (len(waitEarly) == 0 || !waitEarly[0]) {
		a.mu.Unlock()
		return s, nil
	}
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		return accessSession{}, ctx.Err()
	case <-flight.done:
	}
	if ctx.Err() != nil {
		return accessSession{}, ctx.Err()
	}
	if flight.err != nil {
		a.mu.Lock()
		current, exists := a.sessions[key]
		usable := exists && !o.broken && !o.closed && !o.quarantined[key] && !o.denied[key] && o.valid(current, a.now()) && a.now().Before(current.renewal.VerifiedUntil)
		a.mu.Unlock()
		if errors.Is(flight.err, errAuthenticationTemporary) && usable {
			return current, nil
		}
		return accessSession{}, flight.err
	}
	// Recheck local revocation and absolute deadline after the shared task.
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok = a.sessions[key]
	if o.broken || o.closed || o.ctx.Err() != nil {
		return accessSession{}, errSessionStorage
	}
	if !ok || o.denied[key] || !o.valid(s, a.now()) {
		return accessSession{}, errLoginRequired
	}
	if !a.now().Before(s.renewal.VerifiedUntil) {
		return accessSession{}, errAuthenticationTemporary
	}
	return s, nil
}

func (o *accessSessionRenewal) update(key [32]byte, expected accessSession, grant authn.Grant, verified bool) (accessSession, error) {
	var result accessSession
	err := o.change(func(next map[[32]byte]accessSession) error {
		current, ok := next[key]
		if !ok || current.Subject != expected.Subject || current.renewal.Revision != expected.renewal.Revision || o.denied[key] {
			return errLoginRequired
		}
		r := *current.renewal
		r.Grant = grant
		r.Revision++
		if grant.AccessExpires.Before(r.VerifiedUntil) {
			r.VerifiedUntil = grant.AccessExpires
		}
		if verified {
			r.VerifiedAt = o.a.now()
			r.VerifiedUntil = r.VerifiedAt.Add(authn.IdentityVerificationWindow)
			if grant.AccessExpires.Before(r.VerifiedUntil) {
				r.VerifiedUntil = grant.AccessExpires
			}
		}
		current.renewal = &r
		next[key] = current
		result = current
		return nil
	})
	return result, err
}

func (o *accessSessionRenewal) run(key [32]byte, session accessSession, flight *renewalFlight) {
	defer o.wg.Done()
	started := time.Now()
	userID := session.Subject.UserID
	ctx, cancel := context.WithTimeout(o.ctx, 35*time.Second)
	defer cancel()
	err := func() error {
		select {
		case o.limit <- struct{}{}:
			defer func() { <-o.limit }()
		case <-ctx.Done():
			return errAuthenticationTemporary
		}
		grant := session.renewal.Grant
		// A newly saved access token survives userinfo outages and restart. Use
		// it again; rotate only when its actual lifetime is almost exhausted.
		lead := grant.AccessExpires.Sub(session.renewal.VerifiedAt) / 4
		if lead > time.Minute {
			lead = time.Minute
		}
		if lead < 0 {
			lead = 0
		}
		if !o.a.now().Before(grant.AccessExpires.Add(-lead)) {
			next, err := o.provider.Refresh(ctx, grant)
			if err != nil {
				return err
			}
			if !validStoredGrant(next) || [4]string{next.Provider, next.Authority, next.Subject, next.ClientID} != [4]string{grant.Provider, grant.Authority, grant.Subject, grant.ClientID} {
				return &authn.Failure{Kind: authn.InvalidGrant}
			}
			var errSave error
			session, errSave = o.update(key, session, next, false)
			if errSave != nil {
				if errors.Is(errSave, errSessionStorage) {
					o.a.mu.Lock()
					o.quarantined[key] = true
					o.a.mu.Unlock()
					o.fence(key)
				}
				return errSave
			}
			grant = next
		}
		id, err := o.provider.Verify(ctx, grant)
		if err != nil {
			return err
		}
		if externalIdentityKey(id) != [3]string{grant.Provider, grant.Authority, grant.Subject} {
			return &authn.Failure{Kind: authn.InvalidGrant}
		}
		_, err = o.update(key, session, grant, true)
		return err
	}()
	var f *authn.Failure
	if errors.As(err, &f) && f.Kind == authn.InvalidGrant {
		if revokeErr := o.revoke(key); revokeErr != nil {
			err = revokeErr
		} else {
			err = errLoginRequired
		}
	} else if err != nil && !errors.Is(err, errLoginRequired) && !errors.Is(err, errSessionStorage) {
		err = errAuthenticationTemporary
	}
	if err != nil {
		category := "temporary"
		if f != nil {
			category = string(f.Kind)
		}
		if errors.Is(err, errSessionStorage) {
			category = "storage"
		}
		slog.Warn("browser authentication check failed", "operation", "renew", "user_id", userID, "category", category, "duration_ms", time.Since(started).Milliseconds())
	}
	a := o.a
	a.mu.Lock()
	if _, exists := a.sessions[key]; err != nil && exists && !o.denied[key] {
		retry := o.retries[key]
		delays := []time.Duration{time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}
		index := retry.failures
		if index >= len(delays) {
			index = len(delays) - 1
		}
		delay := delays[index]
		if n, e := rand.Int(rand.Reader, big.NewInt(int64(delay/4)+1)); e == nil {
			delay += time.Duration(n.Int64())
		}
		if f != nil && f.RetryAfter > delay {
			delay = f.RetryAfter
		}
		if delay > time.Minute {
			delay = time.Minute
		}
		o.retries[key] = renewalRetry{retry.failures + 1, a.now().Add(delay), err}
	} else {
		delete(o.retries, key)
	}
	flight.err = err
	delete(o.flights, key)
	close(flight.done)
	a.mu.Unlock()
}

func (o *accessSessionRenewal) close(ctx context.Context) error {
	o.a.mu.Lock()
	o.closed = true
	o.cancel()
	o.a.revokeUIUpdatesLocked(true)
	o.a.mu.Unlock()
	done := make(chan struct{})
	go func() { o.wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	// Drain a login/logout snapshot already being committed before writer close.
	for !o.commit.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	o.commit.Unlock()
	return nil
}

// Match the shared 35s IAM budget without consuming/replaying a business body.
func authenticationWait(w http.ResponseWriter) func() {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(45 * time.Second))
	_ = controller.SetWriteDeadline(time.Now().Add(50 * time.Second))
	return func() {
		_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
	}
}
func (a *serverAccess) authenticateBrowserHTTP(w http.ResponseWriter, r *http.Request) (accessSession, error) {
	if a.authority().renewal != nil {
		reset := authenticationWait(w)
		defer reset()
	}
	return a.authenticateBrowser(r)
}

func (a *serverAccess) authenticateBrowser(r *http.Request) (accessSession, error) {
	a = a.authority()
	c, err := r.Cookie(a.cookieName())
	if err != nil || !validAccessToken(c.Value) {
		return accessSession{}, errLoginRequired
	}
	if a.renewal != nil {
		return a.renewal.authenticate(r.Context(), tokenDigest(c.Value))
	}
	subject, ok := a.browserSubject(r)
	if !ok {
		return accessSession{}, errLoginRequired
	}
	return accessSession{Subject: subject}, nil
}

func writeAuthenticationError(w http.ResponseWriter, err error) {
	if errors.Is(err, errLoginRequired) {
		accessError(w, 401, "login_required")
		return
	}
	w.Header().Set("Retry-After", "1")
	if errors.Is(err, errSessionStorage) {
		accessError(w, 503, "session_storage_unavailable")
		return
	}
	if errors.Is(err, errSessionCapacity) {
		accessError(w, 503, "session_capacity_reached")
		return
	}
	accessError(w, 503, "authentication_temporarily_unavailable")
}

func (a *serverAccess) serveRenewableControl(w http.ResponseWriter, r *http.Request, origin string) bool {
	o := a.authority().renewal
	if o == nil {
		return false
	}
	path := r.URL.Path
	if path != "/v1/access/logout" && path != "/v1/access/session" && path != "/v1/access/session/renew" {
		return false
	}
	reset := authenticationWait(w)
	defer reset()
	if path == "/v1/access/session" && r.Method != http.MethodGet || path != "/v1/access/session" && r.Method != http.MethodPost {
		accessError(w, 405, "method_not_allowed")
		return true
	}
	if r.Method == http.MethodPost {
		if r.Header.Get("Origin") != origin {
			accessError(w, 403, "invalid_origin")
			return true
		}
		var body struct{}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || dec.Decode(new(any)) != io.EOF {
			accessError(w, 400, "invalid_request")
			return true
		}
	}
	if path == "/v1/access/logout" {
		if c, err := r.Cookie(o.a.cookieName()); err == nil {
			if err := o.revoke(tokenDigest(c.Value)); err != nil {
				writeAuthenticationError(w, err)
				return true
			}
		}
		http.SetCookie(w, o.a.cookie("", -1))
		writeAccessSession(w, "")
		return true
	}
	var s accessSession
	var err error
	if cookie, cookieErr := r.Cookie(o.a.cookieName()); cookieErr == nil && validAccessToken(cookie.Value) {
		s, err = o.authenticate(r.Context(), tokenDigest(cookie.Value), path == "/v1/access/session/renew")
	} else {
		err = errLoginRequired
	}
	if err != nil {
		writeAuthenticationError(w, err)
		return true
	}
	if r.Context().Err() != nil {
		return true
	}
	after := renewAfter(s.renewal)
	o.a.mu.Lock()
	if cookie, err := r.Cookie(o.a.cookieName()); err == nil {
		if retry := o.retries[tokenDigest(cookie.Value)]; retry.after.After(after) {
			after = retry.after
		}
	}
	o.a.mu.Unlock()
	space := o.a.defaultSpace(s.Subject)
	_ = writeJSON(w, map[string]any{"operator": s.Subject.UserID, "user_id": s.Subject.UserID, "auth_version": s.Subject.AuthVersion, "space_id": space, "installation_id": a.installationID, "login_enabled": true, "deployment_managed": true,
		"renewal_supported": true, "absolute_expires_at": s.expires, "verified_until": s.renewal.VerifiedUntil, "renew_after": after})
	return true
}

func (s *spaceServer) admitRenewable(ctx context.Context, w http.ResponseWriter, login authn.Login, request *http.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return ErrClosing
	}
	entry, err := s.access.users.reserve(login.Identity, s.access.config.Deployment.Authentication)
	if err == nil {
		err = s.publishDefaultSpaceLocked(entry)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	var old [32]byte
	if cookie, err := request.Cookie(s.access.cookieName()); err == nil {
		old = tokenDigest(cookie.Value)
	}
	token, err := s.access.renewal.install(login, userSubject{entry.ID, entry.AuthVersion}, old)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		_ = s.access.renewal.revoke(tokenDigest(token))
		return ctx.Err()
	}
	s.access.mu.Lock()
	installed, ok := s.access.sessions[tokenDigest(token)]
	s.access.mu.Unlock()
	if !ok || s.access.authorizeSubject(installed.Subject) != nil {
		return errLoginRequired
	}
	http.SetCookie(w, s.access.cookie(token, int(installed.expires.Sub(s.access.now())/time.Second)))
	return nil
}
