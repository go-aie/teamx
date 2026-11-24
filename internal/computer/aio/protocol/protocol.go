// Package protocol contains only the private AIO management wire envelope.
package protocol

import "github.com/go-aie/teamx/internal/computer"

type Request struct {
	Admission   computer.MemberAdmission `json:"admission"`
	Claim       computer.Claim           `json:"claim"`
	Binding     computer.Binding         `json:"binding"`
	Screen      computer.ScreenInfo      `json:"screen"`
	Call        computer.Call            `json:"call"`
	Scope       computer.Scope           `json:"scope"`
	OperationID string                   `json:"operation_id"`
	Control     computer.ControlRequest  `json:"control"`
	View        computer.ViewRequest     `json:"view"`
	Asset       string                   `json:"asset"`
}
