package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const TodosProtocolVersion = 1
const maxTodoOutput = 4 << 20

// TodoQuery is one page of one remote state, not a time window or cursor.
// ProjectID=0 means all accessible projects. The host validates project scope.
type TodoQuery struct {
	State         string
	Page, PerPage int
	ProjectID     int64
	MaxBytes      int64
	Timeout       time.Duration
}

type TodoActor struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type Todo struct {
	ID            int64      `json:"id"`
	RecipientID   int64      `json:"recipient_id"`
	ProjectID     int64      `json:"project_id"`
	Project       string     `json:"project"`
	Action        string     `json:"action"`
	Reason        string     `json:"reason"`
	Kind          string     `json:"kind"`
	TargetType    string     `json:"target_type"`
	TargetID      int64      `json:"target_id"`
	TargetIID     int64      `json:"target_iid"`
	Actor         *TodoActor `json:"actor"`
	TargetAuthor  *TodoActor `json:"target_author"`
	Title         string     `json:"title"`
	Body          string     `json:"body,omitempty"`
	BodyTruncated bool       `json:"body_truncated,omitempty"`
	URL           string     `json:"url"`
	CreatedAt     time.Time  `json:"created_at"`
	State         string     `json:"state"`
	SkipReason    string     `json:"skip_reason"`
}

type TodoBatch struct {
	Version           int    `json:"version"`
	Todos             []Todo `json:"todos"`
	NextPage          int    `json:"next_page"`
	Complete          bool   `json:"complete"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	ResponseBytes     int64  `json:"response_bytes"`
}

type TodoQueryError struct {
	Code              string `json:"code"`
	HTTPStatus        int    `json:"http_status"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
}

func (e *TodoQueryError) Error() string {
	return fmt.Sprintf("GitLab todos query failed (%s, HTTP %d)", e.Code, e.HTTPStatus)
}

type TodoFetcher interface {
	FetchTodos(context.Context, TodoQuery) (TodoBatch, error)
}

// GoTodoFetcher queries the authenticated personal inbox without a Workspace,
// project lookup, subprocess, persistent query resources or write API.
type GoTodoFetcher struct {
	origin      string
	recipientID int64
	read        ReadAPI
}

func NewTodoFetcher(origin string, recipientID int64, read ReadAPI) (*GoTodoFetcher, error) {
	if !validOrigin(origin) || recipientID <= 0 || read == nil {
		return nil, errors.New("invalid GitLab todo account")
	}
	return &GoTodoFetcher{origin: origin, recipientID: recipientID, read: read}, nil
}
func (f *GoTodoFetcher) FetchTodos(ctx context.Context, q TodoQuery) (TodoBatch, error) {
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PerPage == 0 {
		q.PerPage = 100
	}
	if q.MaxBytes == 0 {
		q.MaxBytes = maxTodoOutput
	}
	if q.Timeout == 0 {
		q.Timeout = 30 * time.Second
	}
	if (q.State != "pending" && q.State != "done") || q.Page < 1 || q.Page > 10000 || q.PerPage < 1 || q.PerPage > 100 || q.ProjectID < 0 || q.MaxBytes < 1 || q.MaxBytes > maxTodoOutput || q.Timeout <= 0 || q.Timeout > 30*time.Second {
		return TodoBatch{}, errors.New("invalid GitLab todo query")
	}
	if err := ctx.Err(); err != nil {
		return TodoBatch{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, q.Timeout)
	defer cancel()
	query := url.Values{"state": {q.State}, "page": {strconv.Itoa(q.Page)}, "per_page": {strconv.Itoa(q.PerPage)}}
	if q.ProjectID > 0 {
		query.Set("project_id", strconv.FormatInt(q.ProjectID, 10))
	}
	response, err := f.read(ctx, "/todos", query, nil, q.MaxBytes)
	if ctx.Err() != nil {
		return TodoBatch{}, ctx.Err()
	}
	if err != nil {
		var e *queryError
		if errors.As(err, &e) {
			return TodoBatch{}, &TodoQueryError{Code: e.Code, HTTPStatus: e.Status, RetryAfterSeconds: e.Retry}
		}
		return TodoBatch{}, err
	}
	var raw []rawTodo
	if decodeJSON(response.Data, &raw) != nil || raw == nil || len(raw) > q.PerPage {
		return TodoBatch{}, errors.New("invalid GitLab todo page")
	}
	b := TodoBatch{Version: TodosProtocolVersion, Todos: []Todo{}, RetryAfterSeconds: retrySeconds(response.Header, time.Now()), ResponseBytes: response.Bytes}
	b.NextPage, err = nextPage(response.Header, q.Page, q.PerPage, len(raw))
	if err != nil {
		return TodoBatch{}, err
	}
	b.Complete = b.NextPage == 0
	seen := map[int64]bool{}
	for _, r := range raw {
		item, err := normalizeTodo(r, f.origin, f.recipientID, q)
		if err != nil || seen[item.ID] {
			return TodoBatch{}, errors.New("invalid GitLab todo identity or scope")
		}
		seen[item.ID] = true
		b.Todos = append(b.Todos, item)
	}
	encoded, err := json.Marshal(b)
	if err != nil || len(encoded) > maxTodoOutput {
		return TodoBatch{}, errors.New("GitLab todo output limit exceeded")
	}
	return b, nil
}

type rawTodo struct {
	ID         int64           `json:"id"`
	Action     string          `json:"action_name"`
	TargetType string          `json:"target_type"`
	State      string          `json:"state"`
	CreatedAt  time.Time       `json:"created_at"`
	Actor      *TodoActor      `json:"author"`
	Body       json.RawMessage `json:"body"`
	URL        json.RawMessage `json:"target_url"`
	Project    *struct {
		ID   int64  `json:"id"`
		Path string `json:"path_with_namespace"`
	} `json:"project"`
	Target json.RawMessage `json:"target"`
}

type rawTodoTarget struct {
	ID        int64      `json:"id"`
	IID       int64      `json:"iid"`
	ProjectID *int64     `json:"project_id"`
	Title     string     `json:"title"`
	Author    *TodoActor `json:"author"`
}

func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func normalizeActor(a *TodoActor) *TodoActor {
	if a != nil {
		a.Name = truncateText(a.Name, 255)
		a.Username = truncateText(a.Username, 255)
	}
	return a
}
func normalizeTodo(r rawTodo, origin string, userID int64, q TodoQuery) (Todo, error) {
	t := Todo{ID: r.ID, RecipientID: userID, Action: truncateText(r.Action, 255), TargetType: truncateText(r.TargetType, 255), State: r.State, CreatedAt: r.CreatedAt.UTC(), Actor: normalizeActor(r.Actor)}
	t.Reason, t.Kind = todoReason(t.Action), todoKind(t.TargetType)
	switch {
	case r.Project == nil:
		t.SkipReason = "no_project"
	case t.Kind == "unknown":
		t.ProjectID, t.Project, t.SkipReason = r.Project.ID, r.Project.Path, "unsupported_target"
	case !strings.HasPrefix(strings.TrimSpace(string(r.Target)), "{"):
		t.ProjectID, t.Project, t.SkipReason = r.Project.ID, r.Project.Path, "target_unavailable"
	default:
		t.ProjectID, t.Project = r.Project.ID, r.Project.Path
		var target rawTodoTarget
		if decodeJSON(r.Target, &target) != nil {
			return Todo{}, errors.New("invalid GitLab todo target")
		}
		var body, link string
		if len(r.Body) > 0 && decodeJSON(r.Body, &body) != nil {
			return Todo{}, errors.New("invalid GitLab todo body")
		}
		_ = decodeJSON(r.URL, &link)
		if target.ProjectID != nil && *target.ProjectID != t.ProjectID {
			return Todo{}, errors.New("GitLab todo target project differs")
		}
		t.TargetID, t.TargetIID, t.Title, t.Body, t.TargetAuthor = target.ID, target.IID, truncateText(target.Title, 4096), truncateText(body, 4096), normalizeActor(target.Author)
		t.BodyTruncated = len([]rune(body)) > 4096
		if trustedTodoURL(link, origin, t.Project, t.TargetType, t.TargetIID) {
			t.URL = link
		} else {
			t.SkipReason = "untrusted_url"
		}
	}
	if r.Project != nil && r.Project.ID <= 0 || !validTodo(t, origin, userID, q) {
		return Todo{}, errors.New("invalid GitLab todo")
	}
	return t, nil
}

func todoReason(action string) string {
	switch action {
	case "mentioned", "directly_addressed":
		return "mentioned"
	case "assigned":
		return "assigned"
	case "review_requested":
		return "review_requested"
	default:
		return "other"
	}
}
func todoKind(target string) string {
	switch target {
	case "MergeRequest":
		return "merge_request"
	case "Issue", "WorkItem":
		return "work_item"
	default:
		return "unknown"
	}
}

var todoProjectPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+$`)
var todoNoteFragment = regexp.MustCompile(`^note_[1-9][0-9]*$`)

func validTodo(t Todo, origin string, recipientID int64, q TodoQuery) bool {
	if t.ID <= 0 || t.RecipientID != recipientID || t.CreatedAt.IsZero() || t.State != q.State || t.Reason != todoReason(t.Action) || t.Kind != todoKind(t.TargetType) || len(t.Action) > 1020 || len(t.TargetType) > 1020 || len(t.Title) > 16384 || len(t.Body) > 16384 || !validTodoActor(t.Actor) || !validTodoActor(t.TargetAuthor) {
		return false
	}
	if t.ProjectID == 0 {
		return q.ProjectID == 0 && t.Project == "" && t.SkipReason == "no_project" && t.TargetID == 0 && t.TargetIID == 0 && t.URL == "" && t.TargetAuthor == nil && t.Title == "" && t.Body == ""
	}
	if t.ProjectID < 0 || q.ProjectID != 0 && t.ProjectID != q.ProjectID || len(t.Project) > 4096 || !todoProjectPath.MatchString(t.Project) {
		return false
	}
	for _, p := range strings.Split(t.Project, "/") {
		if p == "." || p == ".." {
			return false
		}
	}
	if t.Kind == "unknown" {
		return t.SkipReason == "unsupported_target" && t.URL == "" && t.TargetID == 0 && t.TargetIID == 0 && t.TargetAuthor == nil && t.Title == "" && t.Body == ""
	}
	if t.SkipReason == "target_unavailable" {
		return t.TargetID == 0 && t.TargetIID == 0 && t.URL == "" && t.TargetAuthor == nil && t.Title == "" && t.Body == ""
	}
	if t.TargetID <= 0 || t.TargetIID <= 0 {
		return false
	}
	if t.SkipReason == "untrusted_url" {
		return t.URL == ""
	}
	return t.SkipReason == "" && trustedTodoURL(t.URL, origin, t.Project, t.TargetType, t.TargetIID)
}
func validTodoActor(a *TodoActor) bool {
	return a == nil || a.ID > 0 && len(a.Name) <= 1020 && len(a.Username) <= 1020
}
func trustedTodoURL(value, origin, project, targetType string, iid int64) bool {
	if len(value) > 8192 {
		return false
	}
	base, fragment, hasFragment := strings.Cut(value, "#")
	if hasFragment && !todoNoteFragment.MatchString(fragment) {
		return false
	}
	routes := []string{"issues", "work_items"}
	if targetType == "MergeRequest" {
		routes = []string{"merge_requests"}
	}
	for _, route := range routes {
		if base == fmt.Sprintf("%s/%s/-/%s/%d", origin, project, route, iid) {
			return true
		}
	}
	return false
}

var _ TodoFetcher = (*GoTodoFetcher)(nil)
