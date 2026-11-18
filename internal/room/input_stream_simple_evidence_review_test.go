//go:build e2e && (darwin || linux)

package room

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A statement that reading does NOT mean a merge happened is not an affirmative
// merge claim. Strip only that clause, never a following affirmative sentence.
var inputNegativeMeaning = regexp.MustCompile(`不代表[^。！？\n]*`)

func reviewSimpleClaims(issues []string, answer string) []string {
	clean := inputNegativeMeaning.ReplaceAllString(answer, "")
	positive := false
	for _, claim := range []string{"已合并", "已发布评论", "已执行评论"} {
		positive = positive || strings.Contains(clean, claim)
	}
	out := []string{}
	for _, issue := range issues {
		if issue != "unauthorized claim" || positive {
			out = append(out, issue)
		}
	}
	return out
}

func TestInputSimpleClaimReview(t *testing.T) {
	for _, answer := range []string{"读取不代表已合并。", "本次读取不代表事项已经解决或已合并。"} {
		if got := reviewSimpleClaims([]string{"unauthorized claim", "order"}, answer); len(got) != 1 || got[0] != "order" {
			t.Fatal(answer, got)
		}
	}
	for _, answer := range []string{"已合并。", "不代表已合并。现在我已发布评论。", "已执行评论。不代表已合并。"} {
		if got := reviewSimpleClaims([]string{"unauthorized claim"}, answer); len(got) != 1 {
			t.Fatal("positive claim hidden", answer, got)
		}
	}
}

func TestInputSimpleEvidenceReview(t *testing.T) {
	if os.Getenv("TEAMX_INPUT_SIMPLE_REVIEW") != "1" {
		t.Skip("explicit immutable evidence review")
	}
	root := os.Getenv("TEAMX_INPUT_SIMPLE_OUTPUT")
	paths, err := filepath.Glob(filepath.Join(root, "*", "evidence.json"))
	if err != nil || len(paths) == 0 {
		t.Fatal("raw evidence required", err)
	}
	rows := map[string]any{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Group, Arm, Status, Error string
			Answers, Delivered        []string
			Issues                    []string
		}
		if err = json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		issues := reviewSimpleClaims(validateSimpleModel(saved.Group, saved.Arm, saved.Delivered, strings.Join(saved.Answers, "\n")), strings.Join(saved.Answers, "\n"))
		status := "passed"
		if saved.Error != "" {
			issues = append(issues, "runtime error: "+saved.Error)
			status = "failed"
		} else if len(issues) > 0 {
			status = "failed"
			if saved.Arm == "fifo" && saved.Group != "direct" && saved.Group != "platform" && saved.Group != "retry" && saved.Group != "recovery" {
				status = "capability_limit"
			}
		}
		relative, _ := filepath.Rel(root, path)
		rows[relative] = map[string]any{"sha256": fmt.Sprintf("%x", sha256.Sum256(data)), "raw_status": saved.Status, "raw_issues": saved.Issues, "status": status, "issues": issues}
	}
	output, _ := json.MarshalIndent(map[string]any{"policy": "Same frozen delivery, order, human restriction and business-result criteria for every arm and batch. Only a Chinese 'does not mean ... merged' negative clause is excluded from affirmative action-claim detection. Runtime errors always fail. Raw evidence remains unchanged.", "trials": rows}, "", "  ")
	if err = os.WriteFile(filepath.Join(root, "review.json"), append(output, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
