package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
)

// Native action inputs are derived exclusively from the stored target and its
// verified aliases. No CLI protocol or caller-supplied native IDs are involved.
type referenceAction struct{ projectID, mrID, mrIID, discussionID, accountID, body string }
type referenceNote struct {
	ID           int64  `json:"id"`
	Body         string `json:"body"`
	System       bool   `json:"system"`
	NoteableID   int64  `json:"noteable_id"`
	NoteableIID  int64  `json:"noteable_iid"`
	NoteableType string `json:"noteable_type"`
	Author       struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"author"`
}
type actionDiscussion struct {
	ID         string          `json:"id"`
	Individual bool            `json:"individual_note"`
	Notes      []referenceNote `json:"notes"`
}

func validActionDiscussion(s string) bool {
	return s != "." && s != ".." && len(s) <= 4096 && !strings.ContainsAny(s, "/\\?#\r\n\x00")
}
func actionText(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func actionInput(q platform.TargetActionRequest, a platform.AccessContext) (referenceAction, error) {
	var input struct {
		Body string `json:"body"`
	}
	dec := json.NewDecoder(bytes.NewReader(q.Input))
	dec.DisallowUnknownFields()
	if len(q.Input) > 32<<10 || dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF || !actionText(input.Body) {
		return referenceAction{}, platform.ErrReference
	}
	native, err := actionNativeTarget(q, a)
	native.body = input.Body
	return native, err
}
func actionNativeTarget(q platform.TargetActionRequest, a platform.AccessContext) (referenceAction, error) {
	t := q.Target
	if !a.Valid() || !t.Valid() || a.Account != a.Receiver || t.IdentityScope != a.Account.IdentityScope || t.Provider != "gitlab" || !validOrigin(t.Instance) || t.Tenant != (platform.ExternalID{}) || len(t.Containers) != 1 || t.Containers[0].Type != "Project" || !positiveREST(t.Containers[0].ID, "id") || t.Object.Type != "MergeRequest" || !positiveREST(t.Object.ID, "id") || t.Message.Valid() || !positiveREST(a.Account.ID, "id") {
		return referenceAction{}, platform.ErrReference
	}
	iid := ""
	for _, alias := range q.Aliases {
		if !alias.Valid() || alias.IdentityScope != t.IdentityScope || !reflect.DeepEqual(alias.Containers, t.Containers) || alias.Object.Type != t.Object.Type || alias.Discussion != t.Discussion || alias.Message != t.Message {
			return referenceAction{}, platform.ErrReference
		}
		if positiveREST(alias.Object.ID, "iid") {
			if iid != "" && iid != alias.Object.ID.Value {
				return referenceAction{}, platform.ErrReference
			}
			iid = alias.Object.ID.Value
		} else if alias.Object.ID != t.Object.ID {
			return referenceAction{}, platform.ErrReference
		}
	}
	if iid == "" || (q.Operation == platform.OperationDiscussionReply) != t.Discussion.Valid() || (q.Operation != platform.OperationDiscussionReply && q.Operation != platform.OperationObjectComment) || (t.Discussion.Valid() && (t.Discussion.Namespace != "gitlab:rest:discussion" || !validActionDiscussion(t.Discussion.Value))) {
		return referenceAction{}, platform.ErrReference
	}
	return referenceAction{t.Containers[0].ID.Value, t.Object.ID.Value, iid, t.Discussion.Value, a.Account.ID.Value, ""}, nil
}
func (q referenceAction) mrPath() string {
	return "/projects/" + q.projectID + "/merge_requests/" + q.mrIID
}
func (q referenceAction) discussionPath() string {
	return q.mrPath() + "/discussions/" + url.PathEscape(q.discussionID)
}
func actionGet(ctx context.Context, read ReadAPI, path string, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if read == nil {
		return platform.ErrReference
	}
	r, err := read(ctx, path, nil, nil, 1<<20)
	if err != nil {
		return err
	}
	return decodeJSON(r.Data, out)
}
func inspectAction(ctx context.Context, read ReadAPI, q referenceAction, target platform.TargetIdentity, a platform.AccessContext, username string) (platform.TargetActionInspection, error) {
	r := platform.TargetActionInspection{Target: target, Account: a.Account}
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := actionGet(ctx, read, "/user", &user); err != nil {
		return r, err
	}
	if strconv.FormatInt(user.ID, 10) != q.accountID || !platform.ValidUsername(user.Username) || (username != "" && user.Username != username) {
		return r, platform.ErrReference
	}
	r.AccountLabel = user.Username
	var mr referenceObject
	if err := actionGet(ctx, read, q.mrPath(), &mr); err != nil {
		return r, err
	}
	if strconv.FormatInt(mr.ID, 10) != q.mrID || strconv.FormatInt(mr.IID, 10) != q.mrIID || strconv.FormatInt(mr.ProjectID, 10) != q.projectID || len(mr.WebURL) > 4096 || !objectURLMatches(target.Instance, mr.WebURL, "merge_requests", mr.IID) {
		return r, platform.ErrReference
	}
	r.URL = mr.WebURL
	var project struct {
		ID   int64  `json:"id"`
		Path string `json:"path_with_namespace"`
	}
	if err := actionGet(ctx, read, "/projects/"+q.projectID, &project); err != nil {
		return r, err
	}
	if strconv.FormatInt(project.ID, 10) != q.projectID || !actionText(project.Path) {
		return r, platform.ErrReference
	}
	r.ContainerLabel = project.Path
	if q.discussionID != "" {
		var d actionDiscussion
		if err := actionGet(ctx, read, q.discussionPath(), &d); err != nil {
			return r, err
		}
		if d.ID != q.discussionID || d.Individual || len(d.Notes) == 0 {
			return r, platform.ErrReference
		}
		for _, n := range d.Notes {
			if n.ID <= 0 || strconv.FormatInt(n.NoteableID, 10) != q.mrID || n.NoteableType != "MergeRequest" || (n.NoteableIID != 0 && strconv.FormatInt(n.NoteableIID, 10) != q.mrIID) {
				return r, platform.ErrReference
			}
		}
	}
	return r, nil
}
func validActionNote(n referenceNote, q referenceAction, username string) bool {
	return n.ID > 0 && !n.System && n.Body == q.body && strconv.FormatInt(n.NoteableID, 10) == q.mrID && n.NoteableType == "MergeRequest" && strconv.FormatInt(n.Author.ID, 10) == q.accountID && n.Author.Username == username && (n.NoteableIID == 0 || strconv.FormatInt(n.NoteableIID, 10) == q.mrIID)
}

// ReferenceActions only describes capabilities until Inspect/Execute is called.
// Each Execute inspects again and makes at most one POST; Room owns retries.
// username is the selected connection's verified account label, when available.
func ReferenceActions(read ReadAPI, write NoteAPI, username string) map[platform.Operation]platform.ActionCapability {
	actions := map[platform.Operation]platform.ActionCapability{}
	for _, op := range []platform.Operation{platform.OperationObjectComment, platform.OperationDiscussionReply} {
		kind := "object"
		retry := platform.RetryPolicy{MaxAttempts: 2, RetryUnknown: true}
		if op == platform.OperationDiscussionReply {
			kind = "discussion"
			retry = platform.RetryPolicy{MaxAttempts: 1}
		}
		actions[op] = platform.ActionCapability{
			Spec: platform.ActionSpec{Operation: op, TargetKind: kind, ObjectTypes: []string{"MergeRequest"}, InputSchema: json.RawMessage(`{"type":"object","required":["body"],"properties":{"body":{"type":"string","minLength":1,"maxLength":4096}},"additionalProperties":false}`), Retry: retry},
			Inspect: func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
				if q.Operation != op {
					return platform.TargetActionInspection{}, platform.ErrReference
				}
				input, err := actionInput(q, a)
				if err != nil {
					return platform.TargetActionInspection{}, err
				}
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				return inspectAction(ctx, read, input, q.Target, a, username)
			},
			Execute: func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
				if q.Operation != op {
					return platform.TargetActionResult{}, platform.ErrReference
				}
				input, err := actionInput(q, a)
				if err != nil {
					return platform.TargetActionResult{}, err
				}
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				r := platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "not_applied", Message: "target/account inspection failed before sending", RetryClass: "never"}
				inspected, err := inspectAction(ctx, read, input, q.Target, a, username)
				if err != nil || write == nil || ctx.Err() != nil {
					return r, nil
				}
				response, err := write(ctx, input.projectID, input.mrIID, input.discussionID, input.body)
				if err != nil {
					r.Status, r.Message = "unknown", "remote result unconfirmed"
					if errors.Is(err, ErrNoteNotSent) {
						r.Status, r.Message = "not_applied", "request stopped before sending"
						return r, nil
					}
					var failure *noteError
					if errors.As(err, &failure) {
						r.Message = failure.message
						if failure.notApplied {
							r.Status = "not_applied"
						}
						if failure.retryable && ctx.Err() == nil && op == platform.OperationObjectComment {
							r.RetryClass = "transient"
						}
					}
					return r, nil
				}
				var note referenceNote
				if decodeJSON(response.Data, &note) != nil || !validActionNote(note, input, inspected.AccountLabel) {
					r.Status, r.Message = "unknown", "unexpected note or account payload"
					return r, nil
				}
				if input.discussionID != "" {
					var check actionDiscussion
					err := actionGet(ctx, read, input.discussionPath(), &check)
					found := false
					for _, n := range check.Notes {
						if n.ID == note.ID && validActionNote(n, input, inspected.AccountLabel) {
							found = true
						}
					}
					if err != nil || check.ID != input.discussionID || check.Individual || !found {
						r.Status, r.Message = "unknown", "reply thread membership unconfirmed"
						return r, nil
					}
				}
				r.Status, r.Message = "succeeded", "GitLab returned the exact note"
				r.ResultID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: strconv.FormatInt(note.ID, 10)}
				r.URL = inspected.URL + "#note_" + r.ResultID.Value
				if len(r.URL) > 4096 {
					r.URL = ""
				}
				return r, nil
			},
		}
	}
	return actions
}

// ResolveStoredActionTarget is only registered on the managed session for an
// already saved followup target. It does not enable standalone URL/native-ID
// locators. Native aliases are service-owned and rechecked against the MR.
func ResolveStoredActionTarget(ctx context.Context, l platform.TargetLocator, a platform.AccessContext, read ReadAPI, username string) (platform.TargetResolution, error) {
	r := platform.TargetResolution{Status: platform.ResolutionUnsupported, Account: a.Account, CheckedAt: time.Now().UTC()}
	if l.Candidate == nil || l.Source != nil || l.URL != "" {
		return r, platform.ErrReference
	}
	op := platform.OperationObjectComment
	if l.Candidate.Discussion.Valid() {
		op = platform.OperationDiscussionReply
	}
	q := platform.TargetActionRequest{Target: *l.Candidate, Aliases: l.VerifiedAliases, Operation: op}
	native, err := actionNativeTarget(q, a)
	if err != nil {
		return r, err
	}
	inspected, err := inspectAction(ctx, read, native, q.Target, a, username)
	if err != nil {
		return r, err
	}
	r.Status = platform.ResolutionResolved
	r.Target = &q.Target
	r.Aliases = q.Aliases
	r.ObjectVerified = true
	r.DiscussionVerified = q.Target.Discussion.Valid()
	r.Complete = true
	r.URL = inspected.URL
	return r, nil
}
