package room

import (
	"crypto/sha256"
	"encoding/hex"
)

type followupAnswer struct {
	Message RoomMessage `json:"message"`
	Basis   string      `json:"basis"`
}

// Outbox receipts survive visible history clearing and close the Room commit gap.
type followupNoticeReceipt struct {
	Seq          uint64 `json:"seq"`
	Author       Author `json:"author"`
	MembershipID string `json:"membership_id"`
	Digest       string `json:"digest"`
}

func noticeDigest(text string) string {
	b := sha256.Sum256([]byte(text))
	return hex.EncodeToString(b[:])
}
