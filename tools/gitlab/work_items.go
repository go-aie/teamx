package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Request/parse logic is adapted from internal/platform/gitlab/read.go,
// work_item.go, assignees.go and write.go. No TeamX business package is imported.
type user struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}
type note struct {
	ID     string `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
	Author *user  `json:"author"`
}
type notePage struct {
	Nodes    []note   `json:"nodes"`
	PageInfo pageInfo `json:"pageInfo"`
}
type discussion struct {
	ID      string   `json:"id"`
	ReplyID string   `json:"replyId"`
	Notes   notePage `json:"notes"`
}
type workItem struct {
	ID           string `json:"id"`
	IID          string `json:"iid"`
	Title        string `json:"title"`
	State        string `json:"state"`
	WorkItemType struct {
		Name string `json:"name"`
	} `json:"workItemType"`
	Widgets []struct {
		Description string `json:"description,omitempty"`
		Assignees   *struct {
			Nodes    []user   `json:"nodes"`
			PageInfo pageInfo `json:"pageInfo"`
		} `json:"assignees,omitempty"`
		Discussions *struct {
			Nodes    []discussion `json:"nodes"`
			PageInfo pageInfo     `json:"pageInfo"`
		} `json:"discussions,omitempty"`
	} `json:"widgets"`
}

func (c *api) workItem(ctx context.Context, o options, fields, decl string, vars map[string]any) (*workItem, error) {
	vars["path"], vars["iid"] = o.repo, o.iid
	query := `query($path: ID!, $iid: String!` + decl + `) { namespace(fullPath: $path) { workItem(iid: $iid) { id iid title state workItemType { name } widgets { ` + fields + ` } } } }`
	var ns struct {
		WorkItem *workItem `json:"workItem"`
	}
	if err := c.graphql(ctx, query, vars, "namespace", false, &ns); err != nil {
		return nil, err
	}
	w := ns.WorkItem
	if w == nil || w.IID != o.iid || !validGID(w.ID, "WorkItem") {
		return nil, errors.New("missing or unexpected work item")
	}
	return w, nil
}

func validGID(id, kind string) bool {
	p := "gid://gitlab/" + kind + "/"
	if !strings.HasPrefix(id, p) {
		return false
	}
	n, e := strconv.ParseInt(strings.TrimPrefix(id, p), 10, 64)
	return e == nil && n > 0
}

const assigneesFields = `... on WorkItemWidgetDescription { description } ... on WorkItemWidgetAssignees { assignees(first: 100, after: $after) { nodes { id username name } pageInfo { hasNextPage endCursor } } }`

func (c *api) workItemAssignees(ctx context.Context, o options) (*workItem, []user, error) {
	after := ""
	seen := map[string]bool{}
	users := []user{}
	ids := map[string]bool{}
	var first *workItem
	for {
		w, err := c.workItem(ctx, o, assigneesFields, ", $after: String", map[string]any{"after": nullable(after)})
		if err != nil {
			return nil, nil, err
		}
		if first == nil {
			first = w
		}
		if first.ID != w.ID {
			return nil, nil, errors.New("work item changed during pagination")
		}
		found := false
		for _, widget := range w.Widgets {
			if p := widget.Assignees; p != nil {
				if found || p.Nodes == nil {
					return nil, nil, errors.New("invalid assignee page")
				}
				found = true
				for _, u := range p.Nodes {
					if !validGID(u.ID, "User") || ids[u.ID] {
						return nil, nil, errors.New("invalid or repeated assignee")
					}
					ids[u.ID] = true
					users = append(users, u)
				}
				after, err = nextCursor(p.PageInfo, seen)
				if err != nil {
					return nil, nil, err
				}
			}
		}
		if !found {
			if after == "" && len(users) == 0 {
				return first, nil, nil
			}
			return nil, nil, errors.New("assignee widget disappeared during pagination")
		}
		if after == "" {
			for i := range first.Widgets {
				if p := first.Widgets[i].Assignees; p != nil {
					p.Nodes = users
					done := false
					p.PageInfo = pageInfo{HasNextPage: &done}
				}
			}
			return first, users, nil
		}
	}
}

const discussionFields = `... on WorkItemWidgetNotes { discussions(first: 50, after: $after) { pageInfo { hasNextPage endCursor } nodes { id replyId notes(first: 100, after: $notesAfter) { pageInfo { hasNextPage endCursor } nodes { id body system author { id username name } } } } } }`

func (c *api) discussionPage(ctx context.Context, o options, after, notesAfter string) ([]discussion, pageInfo, error) {
	w, err := c.workItem(ctx, o, discussionFields, ", $after: String, $notesAfter: String", map[string]any{"after": nullable(after), "notesAfter": nullable(notesAfter)})
	if err != nil {
		return nil, pageInfo{}, err
	}
	for _, widget := range w.Widgets {
		if p := widget.Discussions; p != nil {
			if p.Nodes == nil {
				return nil, pageInfo{}, errors.New("invalid discussion page")
			}
			return p.Nodes, p.PageInfo, nil
		}
	}
	return nil, pageInfo{}, errors.New("work item has no notes widget")
}

// Fetch every outer page and every nested note page. A broken cursor fails the
// command instead of silently reporting a complete, truncated discussion list.
func (c *api) discussions(ctx context.Context, o options) ([]discussion, error) {
	all := []discussion{}
	after := ""
	seen := map[string]bool{}
	ids := map[string]bool{}
	for {
		rows, p, err := c.discussionPage(ctx, o, after, "")
		if err != nil {
			return nil, err
		}
		for _, d := range rows {
			if d.ID == "" || ids[d.ID] || d.Notes.Nodes == nil {
				return nil, errors.New("invalid or repeated discussion")
			}
			ids[d.ID] = true
			notesSeen := map[string]bool{}
			noteIDs := map[string]bool{}
			pending := d.Notes.Nodes
			for {
				for _, n := range pending {
					if n.ID == "" || noteIDs[n.ID] {
						return nil, errors.New("invalid or repeated note")
					}
					noteIDs[n.ID] = true
				}
				next, err := nextCursor(d.Notes.PageInfo, notesSeen)
				if err != nil {
					return nil, err
				}
				if next == "" {
					break
				}
				more, _, err := c.discussionPage(ctx, o, after, next)
				if err != nil {
					return nil, err
				}
				found := false
				for _, m := range more {
					if m.ID == d.ID {
						if m.Notes.Nodes == nil {
							return nil, errors.New("missing notes page")
						}
						pending = m.Notes.Nodes
						d.Notes.Nodes = append(d.Notes.Nodes, m.Notes.Nodes...)
						d.Notes.PageInfo = m.Notes.PageInfo
						found = true
						break
					}
				}
				if !found {
					return nil, errors.New("discussion changed during note pagination")
				}
			}
			all = append(all, d)
		}
		after, err = nextCursor(p, seen)
		if err != nil {
			return nil, err
		}
		if after == "" {
			return all, nil
		}
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func workItemCommand(action string) func(context.Context, *app, []string) error {
	return func(ctx context.Context, a *app, args []string) error {
		path := "work-items " + action
		if action == "discussions" {
			path += " list"
		}
		o, help, err := a.options(path, action, args)
		if err != nil || help {
			return err
		}
		c, err := a.api()
		if err != nil {
			return err
		}
		if action == "discussions" {
			d, err := c.discussions(ctx, o)
			if err != nil {
				return err
			}
			return a.output(d)
		}
		if action == "view" || action == "assignees" {
			w, users, err := c.workItemAssignees(ctx, o)
			if err != nil {
				return err
			}
			if action == "view" {
				return a.output(map[string]any{"workItem": w, "assignees": users})
			}
			if users == nil {
				return errors.New("work item has no assignee widget")
			}
			requested, _ := userIDs(o.users)
			target := []string{}
			for _, u := range users {
				target = append(target, u.ID)
			}
			switch o.mode {
			case "replace":
				target = requested
			case "clear":
				target = []string{}
			case "add":
				for _, id := range requested {
					if !slices.Contains(target, id) {
						target = append(target, id)
					}
				}
			case "remove":
				target = slices.DeleteFunc(target, func(id string) bool { return slices.Contains(requested, id) })
			}
			var result struct {
				WorkItem *workItem `json:"workItem"`
			}
			err = c.graphql(ctx, `mutation($input: WorkItemUpdateInput!) { workItemUpdate(input: $input) { errors workItem { id iid } } }`, map[string]any{"input": map[string]any{"id": w.ID, "assigneesWidget": map[string]any{"assigneeIds": target}}}, "workItemUpdate", true, &result)
			if err != nil {
				return err
			}
			if result.WorkItem == nil || result.WorkItem.ID != w.ID {
				return errors.New("missing mutation work item; result unconfirmed")
			}
			// Check exact effective assignments: server tiers can silently limit sets.
			_, actual, err := c.workItemAssignees(ctx, o)
			if err != nil {
				return err
			}
			if actual == nil {
				return errors.New("assignee widget missing after mutation; result unconfirmed")
			}
			got := []string{}
			for _, u := range actual {
				got = append(got, u.ID)
			}
			slices.Sort(got)
			slices.Sort(target)
			if !slices.Equal(got, target) {
				return errors.New("actual assignees differ from requested set; no retry")
			}
			return a.output(map[string]any{"id": w.ID, "assignees": actual})
		}
		w, err := c.workItem(ctx, o, `... on WorkItemWidgetDescription { description }`, "", map[string]any{})
		if err != nil {
			return err
		}
		if action == "close" {
			var result struct {
				WorkItem *workItem `json:"workItem"`
			}
			err = c.graphql(ctx, `mutation($input: WorkItemUpdateInput!) { workItemUpdate(input: $input) { errors workItem { id iid state } } }`, map[string]any{"input": map[string]any{"id": w.ID, "stateEvent": "CLOSE"}}, "workItemUpdate", true, &result)
			if err != nil {
				return err
			}
			if result.WorkItem == nil || result.WorkItem.ID != w.ID || !strings.EqualFold(result.WorkItem.State, "closed") {
				return errors.New("close not confirmed by mutation response")
			}
			return a.output(result.WorkItem)
		}
		input := map[string]any{"noteableId": w.ID, "body": o.body}
		if o.discussion != "" {
			input["discussionId"] = o.discussion
		}
		var result struct {
			Note json.RawMessage `json:"note"`
		}
		err = c.graphql(ctx, `mutation($input: CreateNoteInput!) { createNote(input: $input) { errors note { id body } } }`, map[string]any{"input": input}, "createNote", true, &result)
		if err != nil {
			return err
		}
		var n note
		if json.Unmarshal(result.Note, &n) != nil || n.ID == "" {
			return fmt.Errorf("note creation not confirmed by mutation response")
		}
		return a.output(result.Note)
	}
}
