package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const EventsProtocolVersion = 1
const maxEventOutput = 4 << 20

// EventQuery describes one page, never a persisted server cursor. Window scans
// use [Start, End); callers own overlapping rescans and durable progress.
// ETag is opt-in, quick-only: the caller must have verified endpoint support and
// retain a durable cache with exactly the same binding/credential/query scope.
type EventQuery struct {
	Mode    string
	Page    int
	PerPage int
	Start   time.Time
	End     time.Time
	ETag    string
}

type EventAuthor struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type Event struct {
	ID             int64       `json:"id"`
	ProjectID      int64       `json:"project_id"`
	Kind           string      `json:"kind"`
	Action         string      `json:"action"`
	TargetType     string      `json:"target_type"`
	TargetID       int64       `json:"target_id"`
	TargetIID      int64       `json:"target_iid"`
	CreatedAt      time.Time   `json:"created_at"`
	Author         EventAuthor `json:"author"`
	Title          string      `json:"title"`
	URL            string      `json:"url"`
	Branch         string      `json:"branch,omitempty"`
	Ref            string      `json:"ref,omitempty"`
	RefType        string      `json:"ref_type,omitempty"`
	SHA            string      `json:"sha,omitempty"`
	BeforeSHA      string      `json:"before_sha,omitempty"`
	DetailsMissing bool        `json:"details_missing,omitempty"`
	RefCount       int         `json:"ref_count,omitempty"`
	NoteID         int64       `json:"note_id,omitempty"`
	NoteBody       string      `json:"note_body,omitempty"`
	NoteableType   string      `json:"noteable_type,omitempty"`
	NoteableID     int64       `json:"noteable_id,omitempty"`
	NoteableIID    int64       `json:"noteable_iid,omitempty"`
	SkipReason     string      `json:"skip_reason,omitempty"`
}

type EventBatch struct {
	Version           int     `json:"version"`
	Events            []Event `json:"events"`
	NextPage          int     `json:"next_page"`
	Complete          bool    `json:"complete"`
	NotModified       bool    `json:"not_modified"`
	ETag              string  `json:"etag"`
	RetryAfterSeconds int     `json:"retry_after_seconds"`
	ResponseBytes     int64   `json:"response_bytes"`
}

// EventFetcher does no scheduling, persistence, filtering or message delivery.
type EventFetcher interface {
	FetchEvents(context.Context, EventQuery) (EventBatch, error)
}

// EventQueryError is safe to display. Raw stderr and GitLab bodies are omitted.
// 401/403 and retry hints are returned to the future subscription scheduler.
type EventQueryError struct {
	Code              string `json:"code"`
	HTTPStatus        int    `json:"http_status"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
}

func (e *EventQueryError) Error() string {
	return fmt.Sprintf("GitLab events query failed (%s, HTTP %d)", e.Code, e.HTTPStatus)
}

// GoEventFetcher owns only an immutable binding and a bounded read boundary.
// Scheduling, durable progress and delivery remain with the existing source.
type GoEventFetcher struct {
	binding Binding
	read    ReadAPI
}

func NewEventFetcher(ctx context.Context, b Binding, read ReadAPI) (*GoEventFetcher, error) {
	if !validEventBinding(b) || read == nil {
		return nil, errors.New("invalid GitLab event binding")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	project, err := readProject(ctx, b, read)
	if err != nil {
		return nil, eventQueryFailure(err)
	}
	b.ProjectID = project.ID
	return &GoEventFetcher{binding: b, read: read}, nil
}
func (f *GoEventFetcher) ProjectID() int64 { return f.binding.ProjectID }

func validEventBinding(b Binding) bool {
	u, err := url.Parse(b.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return false
	}
	if b.Version != 1 || b.RoomID == "" || b.RoomInstance == "" || !regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+$`).MatchString(b.Project) {
		return false
	}
	for _, p := range strings.Split(b.Project, "/") {
		if p == "." || p == ".." {
			return false
		}
	}
	if b.ConnectionID != "" {
		return regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(b.ConnectionID) && regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(b.ConnectionGeneration) && b.ProjectID > 0 && b.TokenEnv == "" && b.CredentialID == "" && b.CredentialFile == ""
	}
	if b.TokenEnv != "" {
		return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`).MatchString(b.TokenEnv) && b.CredentialID == "" && b.CredentialFile == ""
	}
	return regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(b.CredentialID) && filepath.IsAbs(b.CredentialFile) && filepath.Base(b.CredentialFile) == b.CredentialID+".json"
}

func (f *GoEventFetcher) FetchEvents(ctx context.Context, q EventQuery) (EventBatch, error) {
	if q.Mode == "" {
		q.Mode = "quick"
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PerPage == 0 {
		q.PerPage = 20
	}
	if (q.Mode != "quick" && q.Mode != "window") || q.Page < 1 || q.Page > 10000 || q.PerPage < 1 || q.PerPage > 100 || len(q.ETag) > 1024 {
		return EventBatch{}, errors.New("invalid GitLab event query")
	}
	for _, c := range q.ETag {
		if c < 32 || c > 126 {
			return EventBatch{}, errors.New("invalid GitLab event validator")
		}
	}
	query := url.Values{"sort": {"desc"}, "page": {strconv.Itoa(q.Page)}, "per_page": {strconv.Itoa(q.PerPage)}}
	if q.Mode == "window" {
		if q.Start.IsZero() || !q.Start.Before(q.End) || q.ETag != "" {
			return EventBatch{}, errors.New("invalid GitLab event window")
		}
		start, end := q.Start.UTC(), q.End.UTC()
		query.Set("after", start.AddDate(0, 0, -1).Format("2006-01-02"))
		query.Set("before", end.AddDate(0, 0, 1).Format("2006-01-02"))
	} else if q.Page != 1 || !q.Start.IsZero() || !q.End.IsZero() {
		return EventBatch{}, errors.New("invalid GitLab quick event query")
	}
	headers := make(http.Header)
	if q.ETag != "" {
		headers.Set("If-None-Match", q.ETag)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, err := f.read(ctx, fmt.Sprintf("/projects/%d/events", f.binding.ProjectID), query, headers, maxEventOutput)
	if ctx.Err() != nil {
		return EventBatch{}, ctx.Err()
	}
	if err != nil {
		return EventBatch{}, eventQueryFailure(err)
	}
	batch := EventBatch{Version: EventsProtocolVersion, Events: []Event{}, ETag: response.Header.Get("ETag"), RetryAfterSeconds: retrySeconds(response.Header, time.Now()), ResponseBytes: response.Bytes}
	if len(batch.ETag) > 1024 {
		return EventBatch{}, errors.New("invalid GitLab event validator")
	}
	if response.Status == http.StatusNotModified {
		if q.ETag == "" {
			return EventBatch{}, errors.New("unexpected GitLab not-modified result")
		}
		batch.NotModified = true
		return batch, nil
	}
	var raw []rawEvent
	if decodeJSON(response.Data, &raw) != nil || raw == nil || len(raw) > q.PerPage {
		return EventBatch{}, errors.New("invalid GitLab event page")
	}
	batch.NextPage, err = nextPage(response.Header, q.Page, q.PerPage, len(raw))
	if err != nil {
		return EventBatch{}, err
	}
	batch.Complete = batch.NextPage == 0
	for _, r := range raw {
		e, err := normalizeEvent(r, f.binding)
		if err != nil {
			return EventBatch{}, err
		}
		if q.Mode != "window" || !e.CreatedAt.Before(q.Start) && e.CreatedAt.Before(q.End) {
			batch.Events = append(batch.Events, e)
		}
	}
	if q.Mode == "window" {
		batch.ETag = ""
	}
	encoded, err := json.Marshal(batch)
	if err != nil || len(encoded) > maxEventOutput {
		return EventBatch{}, errors.New("GitLab event output limit exceeded")
	}
	return batch, nil
}
func eventQueryFailure(err error) error {
	var e *queryError
	if errors.As(err, &e) {
		return &EventQueryError{Code: e.Code, HTTPStatus: e.Status, RetryAfterSeconds: e.Retry}
	}
	return err
}

type rawEvent struct {
	ID             int64           `json:"id"`
	ProjectID      int64           `json:"project_id"`
	Action         string          `json:"action_name"`
	TargetType     string          `json:"target_type"`
	TargetID       int64           `json:"target_id"`
	TargetIID      int64           `json:"target_iid"`
	CreatedAt      time.Time       `json:"created_at"`
	Author         EventAuthor     `json:"author"`
	AuthorUsername string          `json:"author_username"`
	Title          string          `json:"target_title"`
	Note           json.RawMessage `json:"note"`
	Push           json.RawMessage `json:"push_data"`
}

type rawEventNote struct {
	ID       int64  `json:"id"`
	Body     string `json:"body"`
	Type     string `json:"noteable_type"`
	TargetID int64  `json:"noteable_id"`
	IID      int64  `json:"noteable_iid"`
}

type rawEventPush struct {
	Ref      string `json:"ref"`
	RefType  string `json:"ref_type"`
	SHA      string `json:"commit_to"`
	Before   string `json:"commit_from"`
	Title    string `json:"commit_title"`
	RefCount int    `json:"ref_count"`
}

func normalizeEvent(r rawEvent, b Binding) (Event, error) {
	e := Event{ID: r.ID, ProjectID: r.ProjectID, Action: r.Action, TargetType: r.TargetType, TargetID: r.TargetID, TargetIID: r.TargetIID, CreatedAt: r.CreatedAt.UTC(), Author: r.Author, Title: r.Title, Kind: "unknown", SkipReason: "unsupported_event"}
	if e.ID <= 0 || e.ProjectID != b.ProjectID || e.CreatedAt.IsZero() {
		return Event{}, errors.New("invalid GitLab event identity or time")
	}
	if e.Author.Username == "" {
		e.Author.Username = r.AuthorUsername
	}
	base := b.URL + "/" + b.Project
	switch {
	case e.TargetType == "MergeRequest" && (e.Action == "opened" || e.Action == "merged" || e.Action == "closed"):
		e.Kind, e.SkipReason = "merge_request", ""
		if e.TargetIID < 0 {
			return Event{}, errors.New("invalid GitLab event target")
		}
		if e.TargetIID > 0 {
			e.URL = fmt.Sprintf("%s/-/merge_requests/%d", base, e.TargetIID)
		}
	case e.TargetType == "Note" && e.Action == "commented on":
		var n rawEventNote
		if decodeJSON(r.Note, &n) != nil || n.ID <= 0 || n.IID < 0 {
			return Event{}, errors.New("invalid GitLab event note")
		}
		e.Kind, e.SkipReason, e.NoteID, e.NoteBody, e.NoteableType, e.NoteableID, e.NoteableIID = "note", "", n.ID, n.Body, n.Type, n.TargetID, n.IID
		route := map[string]string{"MergeRequest": "merge_requests", "Issue": "issues", "Snippet": "snippets"}[n.Type]
		if route != "" && n.IID > 0 {
			e.URL = fmt.Sprintf("%s/-/%s/%d#note_%d", base, route, n.IID, n.ID)
		}
	case strings.HasPrefix(strings.TrimSpace(string(r.Push)), "{"):
		var p rawEventPush
		if decodeJSON(r.Push, &p) != nil {
			return Event{}, errors.New("invalid GitLab push event")
		}
		e.Kind, e.SkipReason, e.Ref, e.RefType, e.SHA, e.BeforeSHA, e.Title, e.RefCount = "push", "", p.Ref, p.RefType, p.SHA, p.Before, p.Title, p.RefCount
		e.DetailsMissing = p.RefCount != 0 && p.Ref == ""
		if p.RefType == "branch" && p.Ref != "" {
			e.Branch = p.Ref
		}
		if p.SHA != "" {
			e.URL = base + "/-/commit/" + url.PathEscape(p.SHA)
		}
	}
	return e, nil
}

var _ EventFetcher = (*GoEventFetcher)(nil)
