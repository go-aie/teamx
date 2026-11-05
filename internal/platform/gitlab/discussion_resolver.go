package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
)

// DiscussionQuery is explicit, bounded member work, never background collection.
// URL is only a candidate note locator; all object identity comes from Source.
type DiscussionQuery struct {
	Source platform.SourceRef `json:"source"`
	URL    string             `json:"url,omitempty"`
}

type NoteDiscussion struct {
	NoteID       platform.ExternalID `json:"note_id"`
	DiscussionID platform.ExternalID `json:"discussion_id"`
	Individual   bool                `json:"individual"`
}

type DiscussionResolution struct {
	Query          DiscussionQuery          `json:"query"`
	CheckedAt      time.Time                `json:"checked_at"`
	Status         string                   `json:"status"` // resolved, partial, unavailable, unsupported, mismatch
	Reason         string                   `json:"reason,omitempty"`
	Object         platform.ObjectReference `json:"object"`
	AccountID      platform.ExternalID      `json:"account_id"`
	Username       string                   `json:"username,omitempty"`
	ObjectURL      string                   `json:"object_url,omitempty"`
	ObjectVerified bool                     `json:"object_verified"`
	Complete       bool                     `json:"complete"`
	Mappings       []NoteDiscussion         `json:"mappings,omitempty"`
	NoteID         platform.ExternalID      `json:"note_id,omitzero"`
	DiscussionID   platform.ExternalID      `json:"discussion_id,omitzero"`
}

type DiscussionGET func(context.Context, string, any) (http.Header, error)

type resolvedObject struct {
	ID        int64  `json:"id"`
	IID       int64  `json:"iid"`
	ProjectID int64  `json:"project_id"`
	WebURL    string `json:"web_url"`
}

func positiveREST(id platform.ExternalID, namespace string) bool {
	n, err := strconv.ParseInt(id.Value, 10, 64)
	return id.Namespace == "gitlab:rest:"+namespace && err == nil && n > 0 && strconv.FormatInt(n, 10) == id.Value
}

// ResolveDiscussions coalesces object lookups and discussion pagination within a
// batch, including an ID-only query followed by its verified IID alias. Callers
// provide an authenticated GET-only transport with a response byte limit.
func ResolveDiscussions(ctx context.Context, origin string, queries []DiscussionQuery, get DiscussionGET) ([]DiscussionResolution, error) {
	if len(queries) == 0 || len(queries) > 25 {
		return nil, fmt.Errorf("discussion batch must contain 1-25 queries")
	}
	_, normalized, err := connection.Normalize("gitlab", origin)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	_, userErr := get(ctx, "/api/v4/user", &user)
	results := make([]DiscussionResolution, 0, len(queries))
	cache := map[string]DiscussionResolution{}
	objects := 0
	for _, q := range queries {
		r := DiscussionResolution{Query: q, CheckedAt: time.Now().UTC(), Status: "unavailable", Reason: "account lookup failed"}
		if userErr != nil || user.ID <= 0 || !platform.ValidUsername(user.Username) {
			results = append(results, r)
			continue
		}
		r.AccountID, r.Username = restID(user.ID, "id"), user.Username
		ref := q.Source.Reference
		_, sourceOrigin, sourceErr := connection.Normalize("gitlab", q.Source.Instance)
		if q.Source.Provider != "gitlab" || sourceErr != nil || sourceOrigin != normalized || ref.RecipientID != r.AccountID {
			r.Status, r.Reason = "mismatch", "instance or receiving account does not match configured tool"
			results = append(results, r)
			continue
		}
		resource := ""
		switch ref.Object.Type {
		case "MergeRequest":
			resource = "merge_requests"
		case "Issue":
			resource = "issues"
		}
		if !ref.Valid() || resource == "" || !positiveREST(ref.ProjectID, "id") || (ref.Object.ID.Value != "" && !positiveREST(ref.Object.ID, "id")) || (ref.Object.IID.Value != "" && !positiveREST(ref.Object.IID, "iid")) || (ref.Object.ID.Value == "" && ref.Object.IID.Value == "") {
			r.Status, r.Reason = "unsupported", "requires native REST project/object IDs for MR or Issue; opaque/GraphQL IDs are not converted"
			results = append(results, r)
			continue
		}
		base := "/api/v4/projects/" + ref.ProjectID.Value + "/" + resource
		alias := ref.Object.IID
		if alias.Value == "" {
			alias = ref.Object.ID
		}
		key := base + "/" + alias.Namespace + "/" + alias.Value
		cached, ok := cache[key]
		if !ok {
			if objects >= 5 {
				r.Status, r.Reason = "partial", "batch object query budget exhausted"
				results = append(results, r)
				continue
			}
			objects++
			cached = r
			var obj resolvedObject
			var lookupErr error
			if ref.Object.IID.Value != "" {
				_, lookupErr = get(ctx, base+"/"+ref.Object.IID.Value, &obj)
			} else {
				// REST list APIs filter by IID, not global ID. Search only bounded project
				// pages and compare exact native IDs; never insert a global ID in an IID path.
				lookupErr = fmt.Errorf("global ID not located within bounded project pages")
				for page := 1; page <= 5; page++ {
					var rows []resolvedObject
					h, e := get(ctx, fmt.Sprintf("%s?scope=all&state=all&per_page=50&page=%d", base, page), &rows)
					if e != nil {
						lookupErr = e
						break
					}
					if len(rows) > 50 {
						break
					}
					for _, row := range rows {
						if restID(row.ID, "id") == ref.Object.ID {
							obj = row
							lookupErr = nil
							break
						}
					}
					if lookupErr == nil || h.Get("X-Next-Page") != strconv.Itoa(page+1) {
						break
					}
				}
			}
			if lookupErr != nil {
				cached.Reason = "object lookup failed or incomplete"
			} else if obj.ID <= 0 || obj.IID <= 0 || restID(obj.ProjectID, "id") != ref.ProjectID || (ref.Object.ID.Value != "" && restID(obj.ID, "id") != ref.Object.ID) || (ref.Object.IID.Value != "" && restID(obj.IID, "iid") != ref.Object.IID) || !objectURLMatches(normalized, obj.WebURL, resource, obj.IID) {
				cached.Status, cached.Reason = "mismatch", "API object identity does not match source"
			} else {
				cached.ObjectVerified = true
				cached.Object = objectReference(ref.Object.Type, obj.ID, obj.IID)
				cached.ObjectURL = obj.WebURL
				cached.Status, cached.Reason = "partial", "discussion pagination not complete"
				seenNotes, seenDiscussions := map[int64]bool{}, map[string]bool{}
				for page := 1; page <= 10; page++ {
					var rows []struct {
						ID         string `json:"id"`
						Individual bool   `json:"individual_note"`
						Notes      []struct {
							ID int64 `json:"id"`
						} `json:"notes"`
					}
					h, e := get(ctx, fmt.Sprintf("%s/%d/discussions?per_page=50&page=%d", base, obj.IID, page), &rows)
					if e != nil {
						cached.Reason = discussionFailure(e)
						break
					}
					valid := len(rows) <= 50
					for _, d := range rows {
						if strings.TrimSpace(d.ID) == "" || len(d.ID) > 4096 || seenDiscussions[d.ID] || len(d.Notes) == 0 {
							valid = false
							break
						}
						seenDiscussions[d.ID] = true
						for _, n := range d.Notes {
							if n.ID <= 0 || seenNotes[n.ID] || len(cached.Mappings) >= 500 {
								valid = false
								break
							}
							seenNotes[n.ID] = true
							cached.Mappings = append(cached.Mappings, NoteDiscussion{restID(n.ID, "id"), platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: d.ID}, d.Individual})
						}
						if !valid {
							break
						}
					}
					if !valid {
						cached.Reason = "invalid, repeated or over-limit discussion response"
						cached.Mappings = nil
						break
					}
					next := h.Get("X-Next-Page")
					if next == "" {
						// GitLab may omit pagination headers; a full page alone proves no end.
						if len(rows) == 50 {
							cached.Reason = "missing pagination boundary on full page"
							break
						}
						cached.Complete = true
						cached.Status = "resolved"
						cached.Reason = ""
						break
					}
					if next != strconv.Itoa(page+1) {
						cached.Reason = "invalid discussion pagination boundary"
						break
					}
				}
			}
			cache[key] = cached
			if cached.ObjectVerified {
				cache[base+"/gitlab:rest:id/"+cached.Object.ID.Value] = cached
				cache[base+"/gitlab:rest:iid/"+cached.Object.IID.Value] = cached
			}
		}
		r = cached
		r.Query = q
		r.NoteID, r.DiscussionID = platform.ExternalID{}, platform.ExternalID{}
		// Cached aliases do not override conflicting evidence from another query.
		if r.ObjectVerified && ((ref.Object.ID.Value != "" && ref.Object.ID != r.Object.ID) || (ref.Object.IID.Value != "" && ref.Object.IID != r.Object.IID)) {
			r.ObjectVerified, r.Complete = false, false
			r.Object = platform.ObjectReference{}
			r.Mappings = nil
			r.Status, r.Reason = "mismatch", "conflicting source ID/IID alias"
		}
		note := ref.NoteID
		if note.Value == "" && r.ObjectVerified {
			note = verifiedURLNote(q.URL, r.ObjectURL)
			if note.Value == "" && strings.Contains(q.URL, "#") {
				r.Status, r.Reason = "partial", "candidate URL or fragment does not match verified object; note remains unknown"
			}
		}
		if note.Value != "" {
			if !positiveREST(note, "id") {
				r.Status, r.Reason = "unsupported", "note namespace is not REST; no numeric/GID conversion"
			} else {
				for _, m := range r.Mappings {
					if m.NoteID == note {
						r.NoteID, r.DiscussionID = m.NoteID, m.DiscussionID
						break
					}
				}
				if r.DiscussionID.Value == "" {
					r.Status = "partial"
					r.Reason = "note not found in returned discussions; absence is not proven"
				}
			}
		}
		if ref.DiscussionID.Value != "" && r.DiscussionID != ref.DiscussionID {
			r.Status, r.Reason = "mismatch", "source discussion does not match verified note"
			r.NoteID, r.DiscussionID = platform.ExternalID{}, platform.ExternalID{}
		}
		// Return only the requested note mapping. The full bounded object map lives
		// in this call's shared query cache, never repeated 25 times in tool output.
		var selected []NoteDiscussion
		for _, m := range r.Mappings {
			if m.NoteID == r.NoteID && m.DiscussionID == r.DiscussionID {
				selected = append(selected, m)
			}
		}
		r.Mappings = selected
		results = append(results, r)
	}
	return results, nil
}

func objectURLMatches(origin, raw, resource string, iid int64) bool {
	u, e := url.Parse(raw)
	return e == nil && u.User == nil && u.Scheme+"://"+strings.ToLower(u.Host) == origin && u.RawQuery == "" && u.Fragment == "" && strings.HasSuffix(u.Path, "/-/"+resource+"/"+strconv.FormatInt(iid, 10))
}
func verifiedURLNote(raw, objectURL string) platform.ExternalID {
	u, e := url.Parse(raw)
	obj, oe := url.Parse(objectURL)
	if e != nil || oe != nil || u.User != nil || u.Scheme != obj.Scheme || !strings.EqualFold(u.Host, obj.Host) || u.EscapedPath() != obj.EscapedPath() || u.RawQuery != "" || !strings.HasPrefix(u.Fragment, "note_") {
		return platform.ExternalID{}
	}
	n, e := strconv.ParseInt(strings.TrimPrefix(u.Fragment, "note_"), 10, 64)
	if e != nil || n <= 0 || u.Fragment != "note_"+strconv.FormatInt(n, 10) {
		return platform.ExternalID{}
	}
	return restID(n, "id")
}

func discussionFailure(err error) string {
	text := err.Error()
	for _, code := range []string{"401", "403", "404", "429"} {
		if strings.Contains(text, "HTTP "+code) {
			return "discussion query HTTP " + code + "; no complete result or absence proof"
		}
	}
	return "discussion query failed or exceeded response/time limit; no complete result or absence proof"
}
