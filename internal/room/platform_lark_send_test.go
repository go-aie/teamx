package room

import (
	"context"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

type sendContractReader struct {
	replyContractReader
	account platform.AccountIdentity
	denied  bool
}

func (f *sendContractReader) DirectUser(_ context.Context, a platform.AccountIdentity, id string) (lark.Chat, string, error) {
	if f.denied || id != "ou_peer" {
		return lark.Chat{}, "", ErrIdentity
	}
	return lark.Chat{ID: "oc_private", Mode: "p2p", TenantKey: a.Tenant.Value}, "同名用户", nil
}
func (f *sendContractReader) SendGroup(_ context.Context, a platform.AccountIdentity, id string) (lark.Chat, error) {
	if f.denied || id != "oc_group" {
		return lark.Chat{}, ErrIdentity
	}
	return lark.Chat{ID: id, Mode: "group", Name: "单个测试群", TenantKey: a.Tenant.Value}, nil
}
func (f *sendContractReader) FindSendTargets(_ context.Context, a platform.AccountIdentity, q platform.TargetQuery) ([]platform.TargetCandidate, bool, error) {
	t := platform.TargetIdentity{IdentityScope: a.IdentityScope, Object: platform.NativeIdentity{Type: "user", ID: platform.ExternalID{Namespace: a.ID.Namespace, Value: "ou_peer"}}}
	other := t
	other.Object.ID.Value = "ou_other"
	return []platform.TargetCandidate{{Target: t, Label: "同名用户"}, {Target: other, Label: "同名用户"}}, false, nil
}
