package lark

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// BindRuntime owns the messages-specific account comparison and query scope.
// It retains only the narrow snapshot host, not the Room manager or Store.
func BindRuntime(source notification.Source, host notification.RuntimeHost) (notification.Runtime, error) {
	search, ok := source.(*lark.SearchSource)
	if !ok || search == nil || host == nil {
		return notification.Runtime{}, fmt.Errorf("lark: search source and runtime host required")
	}
	bound := *search
	if bound.Reader == nil {
		bound.Reader = func(q lark.QueryScope) (lark.MessageReader, error) {
			return (*lark.ClientFactory)(nil).New(q.Profile)
		}
	}
	return notification.Runtime{
		Source: &bound,
		Admit: func(_ context.Context, state notification.State) (notification.Scope, error) {
			s, ok := state.(lark.State)
			if !ok {
				return notification.Scope{}, notification.ErrInvalid
			}
			if s.Retired {
				return notification.Scope{}, notification.ErrIdentity
			}
			a, err := host.Account(s.RoomIdentity, s.Binding.PlatformID)
			if err != nil {
				return notification.Scope{}, err
			}
			if a.Generation != s.Binding.Generation || a.Identity != s.Binding.Account {
				return notification.Scope{}, notification.ErrConflict
			}
			binding := lark.Binding{PlatformID: a.PlatformID, Generation: a.Generation, Account: a.Identity, CredentialVersion: a.CredentialVersion, Fingerprint: a.CredentialFingerprint}
			if binding != s.Binding || a.CLIProfile == nil {
				return notification.Scope{}, platform.ErrCredential
			}
			q := lark.QueryScope{Binding: s.Binding, Profile: *a.CLIProfile, PlatformVersion: a.PlatformVersion}
			data, _ := json.Marshal(q)
			return notification.Scope{Data: data, Revision: fmt.Sprintf("%d|%s", a.PlatformVersion, data), CredentialStamp: fmt.Sprintf("%d|%s", a.CredentialVersion, a.CredentialFingerprint)}, nil
		},
		CheckLoaded: func(owner platform.RoomIdentity) error {
			for _, state := range host.States(owner) {
				s, ok := state.(lark.State)
				if !ok || s.Validate(owner) != nil {
					return notification.ErrInvalid
				}
			}
			return nil
		},
	}, nil
}

// SourceBindings carries the account-scoped reader constructor. The original
// admission gate supplies its scope; binding does not read accounts or query.
type SourceBindings struct {
	Reader func(lark.QueryScope) (lark.MessageReader, error)
}

func BindSource(source notification.Source, bindings SourceBindings) (notification.Source, error) {
	if source, ok := source.(*lark.SearchSource); ok && source != nil {
		bound := *source
		bound.Reader = bindings.Reader
		return &bound, nil
	}
	return nil, fmt.Errorf("lark: unsupported notification source %T", source)
}
