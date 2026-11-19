package room

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type followupAnswer struct {
	Message RoomMessage `json:"message"`
	Basis   string      `json:"basis"`
}

// Outbox receipts survive visible history clearing and close the Room commit gap.
type publicationReceipt struct {
	Seq          uint64 `json:"seq"`
	Author       Author `json:"author"`
	MembershipID string `json:"membership_id"`
	Digest       string `json:"digest"`
}

func noticeDigest(text string) string {
	b := sha256.Sum256([]byte(text))
	return hex.EncodeToString(b[:])
}

// Preserve the existing persisted reminder receipts while sharing their contract.
type followupNoticeReceipt = publicationReceipt

func validPublicationID(key, membership string) bool {
	if strings.HasPrefix(key, "followup-"+membership+"-") {
		return true
	}
	suffix, ok := strings.CutPrefix(key, "calendar-"+membership+"-")
	return ok && len(suffix) == 32 && strings.Trim(suffix, "0123456789abcdef") == ""
}
