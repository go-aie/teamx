package inputstreamtest

import (
	"fmt"
	"github.com/go-aie/teamx/internal/platform"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-aie/teamx/internal/platform/notification"
)

func GitLabCapability() *InputCapability {
	return &InputCapability{Descriptor: Descriptor{Provider: "gitlab", Family: "object", Channels: []string{"events", "todos"}, Codec: "gitlab.notification", Schema: 1, Policy: "platform/v1"}, GroupStoredInput: GroupGitLab}
}

// GroupGitLab never treats a REST ID as an IID or a note as its parent.
// Saved Object.IID is already the collector's ID-to-IID evidence.
func GroupGitLab(item notification.Item) (Group, error) {
	g := StoredInputGroup(item)
	s := item.Source
	r := s.Reference
	if s.Provider != "gitlab" {
		return g, ErrInvalid
	}
	u, err := url.Parse(s.Instance)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return g, ErrInvalid
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	g.Instance = u.String()
	if !frozenPositiveREST(r.RecipientID, "id") {
		g.Account = ""
	} else {
		g.Facts.AccountLabel = r.RecipientID.Value
	}
	if g.Tenant == "" {
		g.Tenant = notification.NativeID(s.Tenant)
	}
	kind := ""
	switch strings.ToLower(r.Object.Type) {
	case "mergerequest", "merge_request":
		kind = "merge_request"
	case "issue":
		kind = "issue"
	}
	if frozenPositiveREST(r.ProjectID, "id") {
		g.Facts.ProjectID = r.ProjectID.Value
	}
	if r.Version == 1 && frozenPositiveREST(r.RecipientID, "id") && frozenPositiveREST(r.ProjectID, "id") && frozenPositiveREST(r.Object.IID, "iid") && kind != "" {
		g.Precision, g.Missing = "context", ""
		g.Context = []string{notification.NativeID(r.ProjectID), kind, notification.NativeID(r.Object.IID)}
		g.Facts.ProjectID, g.Facts.ObjectType, g.Facts.IID = r.ProjectID.Value, kind, r.Object.IID.Value
	}
	view := notification.Project(item)
	if view.Context != nil && view.Context.Name != "" {
		g.Facts.Label = view.Context.Name
	}
	if g.Facts.Label == "" && g.Precision == "context" {
		g.Facts.Label = fmt.Sprintf("%s / %s %s", s.Project, kind, r.Object.IID.Value)
	}
	g.Facts.Incomplete = view.Truncated || view.MetadataTruncated
	if s.SubscriptionID == "" && g.Precision == "source" {
		return g, ErrInvalid
	}
	return g, g.Validate()
}

func frozenPositiveREST(id platform.ExternalID, kind string) bool {
	n, err := strconv.ParseInt(id.Value, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id.Value && id.Namespace == "gitlab:rest:"+kind
}
