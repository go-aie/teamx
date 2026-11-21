package notification

import (
	"github.com/go-aie/teamx/internal/inputstream"
	internal "github.com/go-aie/teamx/internal/platform/notification"
)

// These values describe only an immutable payload's local encoding and grouping.
// They expose no Repository, writer, lease or storage backend.
type InputCapability = internal.InputCapability
type InputCodec = inputstream.Codec
type InputDescriptor = inputstream.Descriptor
type InputProjection = inputstream.Projection
type InputGroup = inputstream.Group
type InputPolicy = inputstream.Policy
type StoredRecord = inputstream.StoredRecord
type AppendRequest = inputstream.AppendRequest

func DefaultInputPolicy() InputPolicy       { return inputstream.PlatformPolicy() }
func InputDigest(b []byte) string           { return inputstream.Digest(b) }
func StoredInputGroup(item Item) InputGroup { return internal.StoredInputGroup(item) }
