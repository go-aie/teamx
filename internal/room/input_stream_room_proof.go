package room

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/go-aie/teamx/internal/inputstream"
)

// A bounded, native same-page proof, committed WITH ReadThrough in room.json.
// It is not another Room cursor. Only Confirm writes it; the last proof for
// each current Membership survives process loss until another confirmation.
type roomReadProof struct {
	Confirmation ReadConfirmation `json:"confirmation"`
	From         uint64           `json:"from"`
	Snapshot     uint64           `json:"snapshot"`
	Entries      []uint64         `json:"entries"`
	Digest       string           `json:"digest"`
}

func (p roomReadProof) digest() string {
	p.Digest = ""
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s RoomSnapshot) validateReadProofs() error {
	if len(s.ReadProofs) > len(s.Members) {
		return fmt.Errorf("too many Room confirmation proofs")
	}
	for membership, p := range s.ReadProofs {
		var member *MemberState
		for i := range s.Members {
			if s.Members[i].MembershipID == membership {
				member = &s.Members[i]
				break
			}
		}
		c := p.Confirmation
		if member == nil || c.RoomIdentity != s.RoomIdentity || c.MembershipID != membership || c.AgentID != member.AgentID || c.ReadReceipt == "" ||
			c.ReadThrough > member.ReadThrough || c.ReadThrough <= p.From || c.ReadThrough > p.Snapshot || p.Snapshot > s.LatestSeq || c.Revision > s.Revision || c.Revision == 0 || p.Digest != p.digest() || len(p.Entries) > maxPageMessages {
			return fmt.Errorf("invalid Room confirmation proof")
		}
		entries := make([]inputstream.Location, len(p.Entries))
		for i, seq := range p.Entries {
			entries[i] = inputstream.Location{Partition: roomPartitionRef(s.RoomIdentity), Seq: seq}
		}
		if err := inputstream.ValidateReadRange(roomPartitionRef(s.RoomIdentity), p.From, c.ReadThrough, p.Snapshot, entries); err != nil {
			return fmt.Errorf("invalid Room proof entries: %w", err)
		}
	}
	return nil
}
