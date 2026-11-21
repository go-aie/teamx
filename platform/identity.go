package platform

import internal "github.com/go-aie/teamx/internal/platform"

// Native evidence and durable ownership values used by notification authors.
// These are the same values used by the application; no parallel identity model.
type RoomIdentity = internal.RoomIdentity
type SourceRef = internal.SourceRef
type ExternalID = internal.ExternalID
type ObjectReference = internal.ObjectReference
type NotificationReference = internal.NotificationReference
type IdentityScope = internal.IdentityScope
type AccountIdentity = internal.AccountIdentity
type CLIProfile = internal.CLIProfile

var ErrCredential = internal.ErrCredential
