package scion

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/scion"
)

// BindRuntime owns the workitems account comparison and query scope.
// It retains only the narrow snapshot host, not the Room manager or Store.
func BindRuntime(source notification.Source, host notification.RuntimeHost) (notification.Runtime, error) {
	selected, ok := source.(*scion.Source)
	if !ok || selected == nil || host == nil {
		return notification.Runtime{}, fmt.Errorf("scion: workitems source and runtime host required")
	}
	bound := *selected
	return notification.Runtime{
		Source: &bound,
		Admit: func(_ context.Context, state notification.State) (notification.Scope, error) {
			s, ok := state.(scion.State)
			if !ok {
				return notification.Scope{}, notification.ErrInvalid
			}
			a, err := host.Account(s.RoomIdentity, s.Binding.PlatformID)
			if err != nil {
				return notification.Scope{}, err
			}
			if a.Generation != s.Binding.Generation || a.Identity != s.Binding.Account {
				return notification.Scope{}, notification.ErrConflict
			}
			binding := scion.Binding{PlatformID: a.PlatformID, Generation: a.Generation, Account: a.Identity, CredentialVersion: a.CredentialVersion, Fingerprint: a.CredentialFingerprint}
			if binding != s.Binding || a.CLIProfile == nil {
				return notification.Scope{}, platform.ErrCredential
			}
			q := scion.QueryScope{Binding: s.Binding, Profile: *a.CLIProfile, PlatformVersion: a.PlatformVersion, Owner: s.RoomIdentity, SubscriptionID: s.SubscriptionID, Target: s.Config.Target}
			data, _ := json.Marshal(q)
			return notification.Scope{Data: data, Revision: fmt.Sprintf("%d|%s", a.PlatformVersion, data), CredentialStamp: fmt.Sprintf("%d|%s", a.CredentialVersion, a.CredentialFingerprint)}, nil
		},
		CheckLoaded: func(owner platform.RoomIdentity) error {
			for _, state := range host.States(owner) {
				s, ok := state.(scion.State)
				if !ok || s.Validate(owner) != nil {
					return notification.ErrInvalid
				}
			}
			return nil
		},
	}, nil
}
