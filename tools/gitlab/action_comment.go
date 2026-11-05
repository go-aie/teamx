package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// This adapter performs ONE attempt. The Room executor owns the retry budget.
// Requests arrive on stdin, never as shell fragments or arbitrary argv.
type commentRequest struct {
	ProjectID string `json:"project_id"`
	MRIID     string `json:"mr_iid"`
	Body      string `json:"body"`
}
type commentInspection struct {
	Status    string `json:"status"`
	ProjectID int64  `json:"project_id"`
	MRID      int64  `json:"mr_id"`
	MRIID     int64  `json:"mr_iid"`
	Project   string `json:"project"`
	URL       string `json:"url"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
}
type commentResult struct {
	Status    string `json:"status"`
	Retryable bool   `json:"retryable"`
	RemoteID  string `json:"remote_id,omitempty"`
	URL       string `json:"url,omitempty"`
	Message   string `json:"message"`
}

func positiveDecimal(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == s
}
func actionMRComment(ctx context.Context, a *app, args []string) error {
	if len(args) != 1 || (args[0] != "inspect" && args[0] != "send") {
		return errors.New("internal MR comment adapter requires inspect or send")
	}
	var q commentRequest
	dec := json.NewDecoder(io.LimitReader(a.in, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&q) != nil || dec.Decode(new(any)) != io.EOF || !positiveDecimal(q.ProjectID) || !positiveDecimal(q.MRIID) || strings.TrimSpace(q.Body) == "" || len(q.Body) > 4096 || strings.ContainsRune(q.Body, 0) {
		return errors.New("invalid MR comment request")
	}
	c, err := a.api()
	if err != nil {
		return err
	}
	path := "/api/v4/projects/" + q.ProjectID + "/merge_requests/" + q.MRIID
	if args[0] == "inspect" {
		var user struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}
		if _, err := c.request(ctx, "GET", "/api/v4/user", nil, &user); err != nil {
			return err
		}
		var mr struct {
			ID        int64  `json:"id"`
			IID       int64  `json:"iid"`
			ProjectID int64  `json:"project_id"`
			URL       string `json:"web_url"`
		}
		if _, err := c.request(ctx, "GET", path, nil, &mr); err != nil {
			return err
		}
		var project struct {
			ID   int64  `json:"id"`
			Path string `json:"path_with_namespace"`
		}
		if _, err := c.request(ctx, "GET", "/api/v4/projects/"+q.ProjectID, nil, &project); err != nil {
			return err
		}
		if mr.ID <= 0 || strconv.FormatInt(mr.IID, 10) != q.MRIID || strconv.FormatInt(mr.ProjectID, 10) != q.ProjectID || project.ID != mr.ProjectID || project.Path == "" || user.ID <= 0 || user.Username == "" || !strings.HasPrefix(mr.URL, c.origin+"/") {
			return errors.New("MR/account inspection mismatch")
		}
		return a.output(commentInspection{"verified", mr.ProjectID, mr.ID, mr.IID, project.Path, mr.URL, user.ID, user.Username})
	}
	var note struct {
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
	_, err = c.request(ctx, "POST", path+"/notes", map[string]string{"body": q.Body}, &note)
	if err != nil {
		result := commentResult{Status: "unknown", Message: err.Error()}
		var failure *apiFailure
		if errors.As(err, &failure) {
			result.Retryable = failure.retryable && ctx.Err() == nil
			if failure.notApplied {
				result.Status = "not_applied"
			}
		}
		return a.output(result)
	}
	// Exit zero or HTTP success alone cannot prove that the intended comment exists.
	if note.ID <= 0 || note.Body != q.Body || note.System || note.NoteableType != "MergeRequest" || strconv.FormatInt(note.NoteableIID, 10) != q.MRIID || note.NoteableID <= 0 || note.Author.ID <= 0 || note.Author.Username == "" {
		return a.output(commentResult{Status: "unknown", Message: "unexpected comment payload; remote result unconfirmed"})
	}
	return a.output(struct {
		commentResult
		MRID     int64  `json:"mr_id"`
		UserID   int64  `json:"user_id"`
		Username string `json:"username"`
	}{commentResult{Status: "succeeded", RemoteID: strconv.FormatInt(note.ID, 10), Message: "GitLab returned the exact ordinary comment"}, note.NoteableID, note.Author.ID, note.Author.Username})
}
