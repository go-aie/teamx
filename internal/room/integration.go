package room

import (
	"math"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform"
)

// Receipts are retained independently of chat, including after Clear/restart.
// Fail closed at the bound; never evict a receipt merely to accept new work.
const maxIntegrationReceipts = 100000

type IntegrationSource = platform.SourceRef

func validIntegrationSource(s IntegrationSource) bool {
	return validText(s.Instance) && validRestartID(s.SubscriptionID) &&
		validText(s.Provider) && validText(s.Channel) && validText(s.ItemKey) && len(s.ItemKey) <= 4096 && s.Reference.Valid()
}

type IntegrationReceipt struct {
	Seq    uint64            `json:"seq"`
	Source IntegrationSource `json:"source"`
}

// This actor message is service-internal. No HTTP/CLI request can construct it.
type postIntegrationMessage struct {
	RoomIdentity
	DeliveryID string
	Text       string
	Source     IntegrationSource
}

func (r *Room) PostIntegration(ctx actorx.Context, msg postIntegrationMessage, reply actorx.Reply[PostReceipt]) error {
	if err := r.writable(msg.RoomIdentity); err != nil {
		return err
	}
	if !validText(msg.DeliveryID) || !validText(msg.Text) || len(msg.Text) > maxTextBytes || !validIntegrationSource(msg.Source) {
		return ErrRequest
	}
	if receipt, ok := r.state.IntegrationReceipts[msg.DeliveryID]; ok {
		source := msg.Source
		if receipt.Source.Reference.Version == 0 {
			// Older receipts already handed off this source notification. Newly
			// preserved API evidence must not make its pending redelivery fail.
			source.Reference = platform.NotificationReference{}
		}
		if receipt.Source.URL == "" {
			source.URL = ""
		}
		if receipt.Source != source {
			return ErrConflict
		}
		reply.Complete(PostReceipt{RoomIdentity: r.state.RoomIdentity, Seq: receipt.Seq, ClientMessageID: msg.DeliveryID}, nil)
		return nil
	}
	bytes := len(msg.Text)
	for _, m := range r.state.Messages {
		bytes += len(m.Text)
	}
	if r.state.LatestSeq == math.MaxUint64 || len(r.state.Messages) >= maxMessages || bytes > maxHistoryBytes || len(r.state.IntegrationReceipts) >= maxIntegrationReceipts {
		return ErrCapacity
	}
	next := r.candidate()
	next.LatestSeq++
	source := msg.Source
	next.Messages = append(next.Messages, RoomMessage{Seq: next.LatestSeq, ClientMessageID: msg.DeliveryID, Author: Author{Kind: "integration", ID: msg.Source.Provider}, Text: msg.Text, Source: &source, CreatedAt: r.now().UTC()})
	if next.IntegrationReceipts == nil {
		next.IntegrationReceipts = map[string]IntegrationReceipt{}
	}
	next.IntegrationReceipts[msg.DeliveryID] = IntegrationReceipt{Seq: next.LatestSeq, Source: source}
	return commitRoom(r, ctx, next, reply, PostReceipt{RoomIdentity: next.RoomIdentity, Seq: next.LatestSeq, ClientMessageID: msg.DeliveryID}, nil)
}
