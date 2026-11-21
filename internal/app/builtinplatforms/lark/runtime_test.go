package lark_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	larkplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/lark"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type runtimeHost struct {
	account notification.Account
	err     error
	states  []notification.State
	reads   int
}

func (h *runtimeHost) Account(platform.RoomIdentity, string) (notification.Account, error) {
	h.reads++
	return h.account, h.err
}
func (h *runtimeHost) States(platform.RoomIdentity) []notification.State { h.reads++; return h.states }

func TestMessagesRuntimeBindsWithoutReadingAndFencesAccountAndCredentials(t *testing.T) {
	owner := platform.RoomIdentity{RoomID: "room", RoomInstance: "instance"}
	binding := lark.Binding{PlatformID: "connection", Generation: "generation", CredentialVersion: 3, Fingerprint: "fingerprint",
		Account: platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: "https://open.feishu.cn"}, ID: platform.ExternalID{Namespace: "lark:open_id:app", Value: "ou_reader"}}}
	profile := platform.CLIProfile{Version: 1, Profile: "fixture", Brand: "feishu"}
	account := notification.Account{PlatformID: binding.PlatformID, Generation: binding.Generation, Identity: binding.Account, CredentialVersion: binding.CredentialVersion, CredentialFingerprint: binding.Fingerprint, PlatformVersion: 7, CLIProfile: &profile}
	host := &runtimeHost{account: account}
	unbound := &lark.SearchSource{}
	runtime, err := larkplatform.Definition().Sources[0].Bind(unbound, host)
	if err != nil || host.reads != 0 || unbound.Reader != nil || runtime.Source == unbound || runtime.Source.Schema() != lark.SearchSchema {
		t.Fatal("runtime binding changed static source or accessed runtime state", err, host.reads)
	}
	saved := lark.State{RoomIdentity: owner, Binding: binding}
	wantError := errors.New("account unavailable")
	for _, tc := range []struct {
		name   string
		change func(*notification.Account)
		want   error
	}{
		{"original", func(*notification.Account) {}, nil},
		{"generation", func(a *notification.Account) { a.Generation = "replacement" }, notification.ErrConflict},
		{"native account", func(a *notification.Account) { a.Identity.ID.Value = "ou_other" }, notification.ErrConflict},
		{"credential version", func(a *notification.Account) { a.CredentialVersion++ }, platform.ErrCredential},
		{"credential fingerprint", func(a *notification.Account) { a.CredentialFingerprint = "replacement" }, platform.ErrCredential},
		{"missing profile", func(a *notification.Account) { a.CLIProfile = nil }, platform.ErrCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host.account = account
			tc.change(&host.account)
			_, err := runtime.Admit(t.Context(), saved)
			if !errors.Is(err, tc.want) {
				t.Fatal(err, tc.want)
			}
		})
	}
	host.account = account
	host.err = wantError
	if _, err := runtime.Admit(context.Background(), saved); !errors.Is(err, wantError) {
		t.Fatal("account error hidden", err)
	}
	host.err = nil
	saved.Retired = true
	before := host.reads
	if _, err := runtime.Admit(t.Context(), saved); !errors.Is(err, notification.ErrIdentity) || host.reads != before {
		t.Fatal("retired subscription queried account", err)
	}
	saved.Retired = false
	first, err := runtime.Admit(t.Context(), saved)
	if err != nil {
		t.Fatal(err)
	}
	host.account.PlatformVersion++ // Metadata edits fence results, not credentials.
	second, err := runtime.Admit(t.Context(), saved)
	if err != nil || first.Revision == second.Revision || first.CredentialStamp != second.CredentialStamp {
		t.Fatal("revision/credential boundaries collapsed", err)
	}
	var query lark.QueryScope
	if json.Unmarshal(second.Data, &query) != nil || query.Binding != binding || query.Profile != profile || query.PlatformVersion != account.PlatformVersion+1 {
		t.Fatal("query binding changed", query)
	}
	// Loading is local validation; it neither resolves an account nor starts a reader.
	if err := runtime.CheckLoaded(owner); err != nil {
		t.Fatal(err)
	}
	host.states = []notification.State{saved}
	if err := runtime.CheckLoaded(owner); !errors.Is(err, notification.ErrInvalid) {
		t.Fatal("malformed loaded state accepted", err)
	}
}
