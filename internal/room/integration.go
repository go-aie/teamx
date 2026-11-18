package room

import "github.com/go-aie/teamx/internal/platform"

// Room event and follow-up receipts survive chat clearing and service restart.
const maxRoomReceipts = 100000

type IntegrationSource = platform.SourceRef

func validIntegrationSource(s IntegrationSource) bool {
	return validText(s.Instance) && validRestartID(s.SubscriptionID) &&
		validText(s.Provider) && validText(s.Channel) && validText(s.ItemKey) && len(s.ItemKey) <= 4096 && s.Reference.Valid()
}

// IntegrationReceipt authenticates historical platform messages in saved transcripts.
type IntegrationReceipt struct {
	Seq    uint64            `json:"seq"`
	Source IntegrationSource `json:"source"`
}
