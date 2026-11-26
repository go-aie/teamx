//go:build e2e

package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

// Explicit operator probe for a stopped installation. Both service locks are
// mandatory: a second process must never rotate the running host's grant. It
// uses a selected user's existing browser session, saves the new grant through
// the ordinary owner, then reopens that store. No business or account state is
// changed, and no token/ID token/provider body enters the receipt or test log.
func TestOpenCSGStoppedSessionRefresh(t *testing.T) {
	path, user, receipt := os.Getenv("TEAMX_OPENCSG_RENEWAL_CONFIG"), os.Getenv("TEAMX_OPENCSG_RENEWAL_USER"), os.Getenv("TEAMX_OPENCSG_RENEWAL_RECEIPT")
	if path == "" || user == "" || receipt == "" {
		t.Skip("set explicit private stopped-installation config, user and receipt paths")
	}
	if !filepath.IsAbs(path) || !filepath.IsAbs(receipt) || !validOperatorID(user) {
		t.Fatal("invalid probe arguments")
	}
	if _, info, err := readSettingsFile(path); err != nil || info == nil || info.Mode().Perm() != 0600 {
		t.Fatal("probe requires a private 0600 regular configuration")
	}
	home := filepath.Dir(path)
	lock, err := lockBundleHome(home)
	if err != nil {
		t.Fatal("installation must be stopped before rotating a browser grant")
	}
	defer unlockDirectory(lock)
	runtimeLock, err := lockDirectoryFile(home, ".spaces-runtime.lock")
	if err != nil {
		t.Fatal("Space runtime owner is still present")
	}
	defer unlockDirectory(runtimeLock)
	c, err := readStartupConfig(path, true, false)
	if err != nil {
		t.Fatal("private deployment configuration unavailable")
	}
	if c.Deployment.Mode != "multi-user" || c.Deployment.Authentication.Provider != "opencsg" {
		t.Fatal("probe requires multi-user OpenCSG")
	}
	endpoint := "http://" + *c.Server.Listen
	var registration struct {
		Version        int    `json:"version"`
		InstallationID string `json:"installation_id"`
		Completed      bool   `json:"completed"`
	}
	if err := readIdentitySnapshot(filepath.Join(home, "multi-user-registration.json"), &registration); err != nil || registration.InstallationID == "" || registration.Version != 1 || !registration.Completed {
		t.Fatal("installation registration unavailable")
	}
	c.installationID = registration.InstallationID
	a, err := newServerAccess(c, endpoint)
	if err != nil {
		t.Fatal("local authority unavailable")
	}
	defer a.users.writer.closeWriter(context.Background())
	p, err := c.Deployment.Authentication.provider()
	if err != nil {
		t.Fatal("provider configuration unavailable")
	}
	provider, ok := p.(authn.RenewableProvider)
	if !ok {
		t.Fatal("provider cannot renew")
	}
	o, err := newAccessSessionRenewal(context.Background(), a, provider)
	if err != nil {
		t.Fatal(err)
	}
	a.renewal = o
	defer func() { _ = o.close(context.Background()); _ = a.sessionStore.closeWriter(context.Background()) }()
	if err = o.restore(); err != nil {
		t.Fatal("private v3 store unavailable")
	}
	var key [32]byte
	var original accessSession
	for k, s := range a.sessions {
		if s.Subject.UserID == user && (original.renewal == nil || s.renewal.Created.After(original.renewal.Created)) {
			key, original = k, s
		}
	}
	if original.renewal == nil {
		t.Fatal("selected user has no valid renewable browser session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	next, err := provider.Refresh(ctx, original.renewal.Grant)
	if err != nil {
		var failure *authn.Failure
		if errors.As(err, &failure) {
			t.Fatalf("real refresh failed: %s", failure.Kind)
		}
		t.Fatal("real refresh failed (credentials omitted)")
	}
	saved, err := o.update(key, original, next, false)
	if err != nil {
		o.fence(key)
		t.Fatal("rotated grant could not be committed; browser login is isolated")
	}
	identity, err := provider.Verify(ctx, next)
	if err != nil {
		t.Fatal("latest grant saved, but real userinfo verification failed (credentials omitted)")
	}
	if externalIdentityKey(identity) != externalIdentityKey(authn.Identity{Provider: next.Provider, Authority: next.Authority, Subject: next.Subject}) {
		_ = o.revoke(key)
		t.Fatal("userinfo identity changed")
	}
	updated, err := o.update(key, saved, next, true)
	if err != nil {
		t.Fatal("verified deadline could not be committed")
	}
	if !updated.expires.Equal(original.expires) {
		t.Fatal("refresh extended absolute deadline")
	}
	if err = o.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = a.sessionStore.closeWriter(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Reopen with a new writer, the supported recovery path. This verifies the
	// persisted grant without reusing or restoring a rotated token from a backup.
	a2, err := newServerAccess(c, endpoint)
	if err != nil {
		t.Fatal("reopened authority unavailable")
	}
	defer a2.users.writer.closeWriter(context.Background())
	o2, err := newAccessSessionRenewal(context.Background(), a2, provider)
	if err != nil {
		t.Fatal(err)
	}
	a2.renewal = o2
	defer func() { _ = o2.close(context.Background()); _ = a2.sessionStore.closeWriter(context.Background()) }()
	if err = o2.restore(); err != nil {
		t.Fatal("reopened private store unavailable")
	}
	restored, exists := a2.sessions[key]
	if !exists || restored.renewal.Grant.AccessToken != next.AccessToken || restored.renewal.Grant.RefreshToken != next.RefreshToken || !restored.renewal.Grant.AccessExpires.Equal(next.AccessExpires) || !restored.expires.Equal(original.expires) {
		t.Fatal("reopened grant/deadline mismatch")
	}
	if _, err = provider.Verify(ctx, restored.renewal.Grant); err != nil {
		t.Fatal("reopened grant rejected by real userinfo")
	}
	summary := map[string]any{"user_id": user, "refresh_verified": true, "userinfo_verified": true, "reopen_verified": true, "absolute_deadline_unchanged": true,
		"access_changed": next.AccessToken != original.renewal.Grant.AccessToken, "refresh_rotated": next.RefreshToken != original.renewal.Grant.RefreshToken,
		"absolute_expires_at": restored.expires, "access_expires_at": next.AccessExpires, "verified_until": restored.renewal.VerifiedUntil, "at": time.Now()}
	writer := &snapshotWriter{ops: defaultSnapshotOps()}
	defer writer.closeWriter(context.Background())
	if err = writer.save(receipt, summary, 16<<10); err != nil {
		t.Fatal("sanitized receipt could not be saved")
	}
	t.Log("real refresh, userinfo, private save and reopen verified; credentials omitted")
}
