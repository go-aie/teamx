package lark

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

type TargetPage struct {
	Status     platform.ResolutionStatus `json:"status"`
	ReasonCode string                    `json:"reason_code,omitempty"`
	Contents   []platform.TargetContent  `json:"contents"`
	Complete   bool                      `json:"complete"`
	Next       string                    `json:"next,omitempty"`
}

// ReadTarget owns the provider request and cursor protocol. The Room owner loads
// the immutable target and rechecks its admitted account before returning data.
func ReadTarget(ctx context.Context, q platform.ReadTargetRequest, access platform.AccessContext, reader lark.MessageReader) (TargetPage, error) {
	page, err := lark.ReadTarget(ctx, q, access, reader)
	if err != nil {
		return TargetPage{}, err
	}
	result := TargetPage{Status: page.Status, ReasonCode: page.ReasonCode, Contents: page.Contents, Complete: page.Complete}
	if page.Next != nil {
		b, _ := json.Marshal(page.Next)
		result.Next = base64.RawURLEncoding.EncodeToString(b)
	}
	return result, nil
}
