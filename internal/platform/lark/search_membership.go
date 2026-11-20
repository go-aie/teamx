package lark

import (
	"context"

	"github.com/go-aie/teamx/internal/platform"
)

// MembershipPage is one bounded page of independent joined-group evidence.
// A readable/public chat and a search hit alone never prove membership.
type MembershipPage struct {
	Joined    bool
	HasMore   bool
	PageToken string
}

func (c *Client) GroupMembership(ctx context.Context, a platform.AccountIdentity, id, token string) (MembershipPage, error) {
	var result MembershipPage
	if !nativeID(id, "oc_") || !validPageToken(token) {
		return result, failure("invalid_request")
	}
	err := c.guarded(ctx, a, func(ctx context.Context) error {
		var wire membershipWire
		params := map[string]any{"member_id_type": "open_id", "page_size": 100}
		if token != "" {
			params["page_token"] = token
		}
		if err := c.api(ctx, "/open-apis/im/v1/chats/"+id+"/members", params, &wire); err != nil {
			return err
		}
		var err error
		result, err = validateMembershipPage(wire, a, token)
		return err
	})
	if err != nil {
		return MembershipPage{}, err
	}
	return result, nil
}

type membershipWire struct {
	Items *[]struct {
		ID     string `json:"member_id"`
		Type   string `json:"member_id_type"`
		Tenant string `json:"tenant_key"`
	} `json:"items"`
	More  *bool  `json:"has_more"`
	Token string `json:"page_token"`
}

func validateMembershipPage(wire membershipWire, a platform.AccountIdentity, token string) (MembershipPage, error) {
	var result MembershipPage
	if wire.Items == nil || wire.More == nil || len(*wire.Items) > 100 || !validPageToken(wire.Token) || *wire.More && (wire.Token == "" || wire.Token == token) {
		return MembershipPage{}, failure("invalid_response")
	}
	seen := map[string]bool{}
	for _, member := range *wire.Items {
		if member.Type != "open_id" || !nativeID(member.ID, "ou_") || seen[member.ID] {
			return MembershipPage{}, failure("invalid_response")
		}
		seen[member.ID] = true
		if member.ID == a.ID.Value {
			if member.Tenant != "" && member.Tenant != a.Tenant.Value {
				return MembershipPage{}, failure("conversation_unverified")
			}
			result.Joined = true
		}
	}
	result.HasMore, result.PageToken = *wire.More, wire.Token
	return result, nil
}
