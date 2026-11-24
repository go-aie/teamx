package room

import (
	"context"
	"github.com/go-aie/teamx/internal/computer"
)

type computerInfoValidation struct {
	InstanceID     string `json:"instance_id"`
	ContractDigest string `json:"tool_contract_digest"`
	MaxScreens     int    `json:"max_screens"`
	Running        int    `json:"running_screens"`
}

func validateComputerProvider(ctx context.Context, p computer.ManagedProvider, provider string, owner computer.Owner) (computerInfoValidation, error) {
	info, err := p.Inspect(ctx)
	if err != nil {
		return computerInfoValidation{}, err
	}
	if err = checkComputerInfo(info, provider, owner); err != nil {
		return computerInfoValidation{}, err
	}
	if _, err = checkComputerInventory(ctx, p, owner); err != nil {
		return computerInfoValidation{}, err
	}
	return computerInfoValidation{info.InstanceID, info.Contract.Digest, info.MaxScreens, info.Running}, nil
}
func checkComputerInfo(info computer.Info, provider string, owner computer.Owner) error {
	contract, err := computer.NewContract(info.Contract.Provider, info.Contract.Versions, info.Contract.Tools)
	if !info.MemberAdmission || err != nil || contract.Digest != info.Contract.Digest || info.Contract.Provider != provider || info.InstanceID == "" || info.BootID == "" || info.MaxScreens < 1 || info.MaxScreens > 256 {
		return computer.Fail(computer.ContractMismatch, "Computer capability contract is invalid")
	}
	if info.Owner != (computer.Owner{}) && info.Owner != owner {
		return computer.Fail(computer.Stale, "Computer belongs to another installation or owner")
	}
	for _, t := range info.Contract.Tools {
		if computerReservedTools[t.Name] {
			return computer.Fail(computer.ContractMismatch, "Native Computer tool name collides with a reserved TeamX tool")
		}
	}
	return nil
}

func checkComputerInventory(ctx context.Context, p computer.ManagedProvider, owner computer.Owner) ([]computer.ScreenInfo, error) {
	screens, err := p.Screens(ctx, owner)
	if err != nil {
		return nil, err
	}
	if len(screens) > 256 {
		return nil, computer.Fail(computer.Invalid, "Computer inventory exceeds supported bound")
	}
	seen := map[string]bool{}
	for _, s := range screens {
		if s.ID == "" || s.Epoch == 0 || s.Binding.Owner != owner || s.Binding.Validate() != nil || seen[s.ID] {
			return nil, computer.Fail(computer.Stale, "invalid owned screen inventory")
		}
		seen[s.ID] = true
	}
	return screens, nil
}
