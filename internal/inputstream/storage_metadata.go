package inputstream

import (
	"encoding/hex"
	"strings"
)

// Partition references bind retained source locations to their original owner
// and provider, independently of whether any body or receipt remains.
func PrivatePartitionRef(m Member, provider string) string {
	return Ref("partition_", m, provider)
}

func SharedPartitionRef(inbox Inbox, provider string) string {
	return Ref("partition_", "inbox", inbox, provider)
}

func validSourceRef(ref string) bool {
	if ref == "" {
		return true
	}
	raw, ok := strings.CutPrefix(ref, "src_")
	if !ok || len(raw) != 64 || strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

// DeliverySourceRef resolves an optional stable reference from the durable
// receipt. A missing receipt inside a published reclaimed prefix is expired.
func (r *Repository) DeliverySourceRef(l Location, channel, deliveryID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return "", ErrCommitUnknown
	}
	for _, p := range r.parts {
		if p.state.Spec.Ref != l.Partition {
			continue
		}
		receipt, ok := p.state.Receipts[deliveryKey(channel, deliveryID)]
		if !ok || receipt.Seq != l.Seq {
			if l.Seq > 0 && l.Seq <= p.state.Reclaimed {
				return "", ErrExpired
			}
			return "", ErrIdentity
		}
		return receipt.SourceRef, nil
	}
	return "", ErrIdentity
}

// Keep the existing source identity used by human attention preferences.
func attentionSourceRef(m Member, provider, source string) string {
	return Ref("scope_", m, provider, source, source)
}
