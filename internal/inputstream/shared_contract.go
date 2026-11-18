package inputstream

// SharedRoot is published with the body range and deduplication receipts. It
// contains no member cursor. A Grant is never reused after closing.
type SharedRoot struct {
	ConfigVersion uint64                 `json:"config_version"`
	Inbox         Inbox                  `json:"inbox"`
	Subscription  string                 `json:"subscription"`
	NextGrant     uint64                 `json:"next_grant"`
	Grants        map[string]SharedGrant `json:"grants"`
	Pages         map[string]SharedPage  `json:"pages"`
}
type SharedGrant struct {
	ID     string `json:"id"`
	Member Member `json:"member"`
	Start  uint64 `json:"start"`
	Status string `json:"status"` // active, closing, closed
}

// SharedPageRequest is also the member's durable preparation intent. Defaults
// and the effective member budget have already been normalized before receipt.
type SharedPageRequest struct {
	Excluded  []uint64  `json:"excluded,omitempty"`
	Inbox     Inbox     `json:"inbox"`
	Partition string    `json:"partition"`
	Member    Member    `json:"member"`
	Grant     string    `json:"grant"`
	Stream    string    `json:"stream"` // physical logical stream
	Execution Execution `json:"execution"`
	Page      string    `json:"page"`
	From      uint64    `json:"from"`
	Limit     int       `json:"limit"`
	Bound     *uint64   `json:"bound,omitempty"`
}
type SharedPage struct {
	HandedOff     bool              `json:"handed_off,omitempty"`
	Request       SharedPageRequest `json:"request"`
	RequestDigest string            `json:"request_digest"`
	Snapshot      Snapshot          `json:"snapshot"`
	Scanned       uint64            `json:"scanned"`
	Through       uint64            `json:"through"`
	Entries       []Location        `json:"entries"`
	Remaining     int               `json:"remaining"`
	Digest        string            `json:"digest"`
	Protection    string            `json:"protection"`
	State         string            `json:"state"` // protected, released, aborted
	Proof         string            `json:"proof,omitempty"`
}

func (p SharedPage) checksum() string {
	p.HandedOff = false
	p.Digest = ""
	p.State = ""
	p.Proof = ""
	return checksumState(p)
}
func (q SharedPageRequest) checksum() string { return checksumState(q) }
func SharedStreamRef(inbox Inbox, m Member, grant, physical string) string {
	return Ref("stream_", inbox, m, grant, physical)
}

type MemberReleaseCandidate struct {
	Inbox        Inbox    `json:"inbox"`
	Member       Member   `json:"member"`
	Grant        string   `json:"grant"`
	Stream       string   `json:"stream"`
	Location     Location `json:"location"`
	RecordDigest string   `json:"record_digest"`
}
type MemberRelease struct {
	Candidate MemberReleaseCandidate `json:"candidate"`
	State     string                 `json:"state"` // unknown, held, released
	Reason    string                 `json:"reason"`
	Revision  uint64                 `json:"revision"`
	Evidence  string                 `json:"evidence,omitempty"`
}
