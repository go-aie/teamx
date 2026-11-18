package room

import (
	"fmt"
	"sort"

	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Read-only adaptation of committed snapshots. The caller obtains a fixed,
// complete retained InboxPage and its consumption observation before calling;
// this function never opens a Store, associates sources or acknowledges input.
// Legacy task_event conversion belongs to the later Room/upgrade stages.
func projectStoredInputRecords(id MemberIdentity, room RoomSnapshot, page InboxPage, consumed inboxConsumption, admissions map[uint64]notification.Admission, capabilities func(string) frozenCapabilities) ([]inputstream.Record, error) {
	if err := room.validate(); err != nil {
		return nil, err
	}
	if room.RoomIdentity != id.RoomIdentity || page.MemberIdentity != id || consumed.MemberIdentity != id || consumed.Version != 1 {
		return nil, ErrIdentity
	}
	var member *MemberState
	for i := range room.Members {
		if room.Members[i].MembershipID == id.MembershipID && room.Members[i].AgentID == id.AgentID {
			member = &room.Members[i]
			break
		}
	}
	if member == nil {
		return nil, ErrIdentity
	}
	if page.HasMore || page.NextAfter != page.SnapshotSeq || consumed.Through > page.SnapshotSeq {
		return nil, ErrRequest
	}
	owner := inputstream.Member{RoomID: id.RoomID, RoomInstance: id.RoomInstance, MembershipID: id.MembershipID}
	out := []inputstream.Record{}
	for _, message := range room.Messages {
		if message.Seq <= member.ReadThrough || message.Author == (Author{Kind: "agent", ID: id.AgentID}) {
			continue
		}
		group := inputstream.RoomGroup(owner, id.RoomID)
		group.Facts.Summary = projectRoomMessage(message).Text
		out = append(out, inputstream.Record{Location: inputstream.Location{Partition: "room/" + id.RoomInstance, Seq: message.Seq}, Group: group, ReceivedAt: message.CreatedAt, Pending: true, Excerpt: group.Facts.Summary})
	}
	for _, entry := range page.Entries {
		if err := entry.validate(id); err != nil {
			return nil, err
		}
		if entry.Seq <= max(consumed.Through, page.ExpiredThrough) || consumed.skipped(entry.Seq) {
			continue
		}
		if entry.payloadType() != InboxPlatformNotification {
			return nil, inputstream.ErrUnsupported
		}
		cap := capabilities(entry.Item.Source.Provider).Input
		if cap == nil || cap.GroupStoredInput == nil {
			return nil, inputstream.ErrUnsupported
		}
		group, err := cap.GroupStoredInput(entry.Item)
		if err != nil {
			return nil, err
		}
		admission := admissions[entry.Seq]
		if !admission.Valid() {
			return nil, ErrInvalid
		}
		if admission.Verified && admission.Decision == notification.AdmissionExcluded {
			continue
		}
		blocked := ""
		if !admission.Verified {
			blocked = admission.ReasonCode
			if blocked == "" {
				blocked = "saved_admission_pending"
			}
		}
		view := notification.Project(entry.Item)
		if project := capabilities(entry.Item.Source.Provider).Project; project != nil {
			view = project(entry.Item)
		}
		out = append(out, inputstream.Record{Location: inputstream.Location{Partition: "legacy-inbox/" + id.MembershipID, Seq: entry.Seq}, Group: group, ReceivedAt: entry.ReceivedAt, Pending: true, Blocked: blocked, Excerpt: view.Text, Incomplete: view.Truncated || view.MetadataTruncated})
	}
	return out, nil
}

// Project the existing static source assembly. This is not a second registry:
// removing/adding a source automatically removes/adds its supported channel.
// Call during composition, outside Store/Actor locks. No remote work occurs.
func inputStreamDescriptors(adapters map[notification.SourceID]notificationAdapter, capabilities func(string) frozenCapabilities) ([]inputstream.Descriptor, error) {
	byProvider := map[string]inputstream.Descriptor{}
	for id, adapter := range adapters {
		if adapter.Source == nil || adapter.Source.ID() != id {
			return nil, inputstream.ErrInvalid
		}
		cap := capabilities(id.Provider).Input
		if cap == nil {
			continue
		}
		d := cap.Descriptor
		if d.Provider != id.Provider || d.Codec == "" || d.Schema < 1 || cap.GroupStoredInput == nil {
			return nil, inputstream.ErrInvalid
		}
		if previous, ok := byProvider[d.Provider]; ok && (previous.Codec != d.Codec || previous.Schema != d.Schema || previous.Family != d.Family || previous.Policy != d.Policy) {
			return nil, fmt.Errorf("%w: inconsistent provider input descriptor", inputstream.ErrInvalid)
		}
		saved := byProvider[d.Provider]
		if saved.Provider == "" {
			saved = d
			saved.Channels = nil
		}
		saved.Channels = append(saved.Channels, id.Channel)
		byProvider[d.Provider] = saved
	}
	providers := []string{}
	for p := range byProvider {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	out := []inputstream.Descriptor{}
	for _, p := range providers {
		d := byProvider[p]
		sort.Strings(d.Channels)
		out = append(out, d)
	}
	return out, nil
}

// Frozen experiment bindings never enter the runtime capabilities or schema.
type frozenCapabilities struct {
	Input   *inputstream.InputCapability
	Project func(notification.Item) notification.View
}

func frozenInputCapabilities(provider string) frozenCapabilities {
	c := notificationCapabilities(provider)
	var input *inputstream.InputCapability
	switch provider {
	case "gitlab":
		input = inputstream.GitLabCapability()
	case "lark":
		input = inputstream.LarkCapability()
	}
	return frozenCapabilities{input, c.Project}
}
