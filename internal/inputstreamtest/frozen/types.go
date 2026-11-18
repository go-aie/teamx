package frozen

import (
	"encoding/json"
	"sort"
	"sync"

	core "github.com/go-aie/teamx/internal/inputstream"
)

type Member = core.Member
type Location = core.Location
type Snapshot = core.Snapshot
type StoredRecord = core.StoredRecord
type PartitionSpec = core.PartitionSpec
type Codec interface {
	Descriptor() Descriptor
	Decode(schema int, b json.RawMessage) (Projection, error)
	NewIndex() NativeIndex
	DescribeInput() Guidance
}

// PlatformQueryCodec adapts canonical records to the archived four-tool query.
func PlatformQueryCodec(c core.Codec) Codec {
	switch c.Descriptor().Provider {
	case "gitlab":
		return gitLabQueryCodec{c}
	case "lark":
		return larkQueryCodec{c}
	default:
		return nil
	}
}

var Ref = core.Ref

const MaxRecords = core.MaxRecords

var ErrInvalid = core.ErrInvalid
var ErrIdentity = core.ErrIdentity
var ErrCapacity = core.ErrCapacity
var ErrConflict = core.ErrConflict
var ErrExpired = core.ErrExpired
var ErrCommitUnknown = core.ErrCommitUnknown
var ErrUnsupported = core.ErrUnsupported

type scopeMetadata struct {
	Key, Source, Label string
	First              uint64
}
type rootState struct {
	Spec                        PartitionSpec
	Revision, Latest, Reclaimed uint64
	Scopes                      []scopeMetadata
}
type partition struct {
	state       rootState
	records     map[uint64]StoredRecord
	projections map[uint64]Projection
	search      map[uint64]string
	native      NativeIndex
}
type Repository struct {
	mu        sync.Mutex
	member    Member
	parts     map[string]*partition
	codecs    map[string]Codec
	uncertain bool
}

func snapshot(p *partition) Snapshot {
	return Snapshot{Partition: p.state.Spec.Ref, Revision: p.state.Revision, Through: p.state.Latest, Reclaimed: p.state.Reclaimed}
}
func sortedSeqs(p *partition) []uint64 {
	out := []uint64{}
	for n := range p.records {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func cloneMap[K comparable, V any](in map[K]V) map[K]V {
	out := map[K]V{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

var StrictDecode = core.StrictDecode
var ValidateRequest = core.ValidateRequest
var DecodeRead = core.DecodeRead

type AppendRequest = core.AppendRequest
type ReadRequest = core.ReadRequest
type Descriptor = core.Descriptor

var ErrCorrupt = core.ErrCorrupt
var ErrVersion = core.ErrVersion

const MaxRecordBytes = core.MaxRecordBytes
