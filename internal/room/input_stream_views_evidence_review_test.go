//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
	"github.com/mark3labs/mcp-go/mcp"
)

// Replay every saved sample with the frozen fixture and both request shapes;
// never overwrite evidence or call a model/platform.
func TestInputViewsEvidenceReview(t *testing.T) {
	root := os.Getenv("TEAMX_INPUT_COMPARISON_REVIEW")
	if root == "" {
		t.Skip("opt-in offline evidence review")
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", "evidence.json"))
	if err != nil || len(paths) == 0 {
		t.Fatal("evidence required", err)
	}
	rows := map[string]any{}
	for _, path := range paths {
		var report struct {
			Scenario string           `json:"scenario"`
			Mode     string           `json:"mode"`
			Status   string           `json:"status"`
			Error    string           `json:"error"`
			Answer   string           `json:"answer"`
			Initial  comparisonState  `json:"initial_state"`
			Final    comparisonState  `json:"final_state"`
			Calls    []comparisonCall `json:"calls"`
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &report); err != nil {
			t.Fatal(err)
		}
		f := viewsComparisonFixture(t, report.Scenario)
		if !reflect.DeepEqual(comparisonSnapshot(f), report.Initial) {
			t.Fatal("initial fixture differs from saved run", path)
		}
		pending := map[inputstream.Location]bool{}
		for _, p := range report.Final.Pending {
			pending[p] = true
		}
		for i := range f.records {
			f.records[i].Pending = pending[f.records[i].Location]
		}
		f.remaining = report.Final.Remaining
		f.reads = report.Final.Delivered
		for _, p := range report.Final.Associations {
			f.associations[p] = true
		}
		issues := validateComparison(report.Scenario, report.Mode, f, report.Initial, report.Calls, report.Answer)
		if strings.HasPrefix(report.Scenario, "ext-") {
			issues = validateViewsExtension(report.Scenario, report.Mode, f, report.Initial, report.Calls, report.Answer)
		}
		originalIssues := append([]string{}, issues...)
		if !strings.HasPrefix(report.Scenario, "ext-") {
			issues = reviewViewsReportLanguage(issues, report.Answer)
			issues = reviewViewsEquivalentViews(report.Scenario, f, report.Initial, report.Calls, issues)
		}
		if report.Error != "" {
			issues = append(issues, "runtime error: "+report.Error)
		}
		status := "passed"
		if len(issues) > 0 {
			status = "failed"
		}
		relative, _ := filepath.Rel(root, path)
		rows[relative] = map[string]any{"original_status": report.Status, "original_issues": originalIssues, "status": status, "issues": issues, "matches_original": status == report.Status, "evidence_sha256": fmt.Sprintf("%x", sha256.Sum256(data))}
	}
	output := os.Getenv("TEAMX_INPUT_COMPARISON_REVIEW_OUTPUT")
	if output == "" {
		t.Fatal("separate output required")
	}
	b, err := json.MarshalIndent(map[string]any{"policy": "Frozen whole-set, ordering, identity, state, restriction and per-view criteria; both schema shapes; negated conditional success phrases excluded only from success-claim detection; known within(stream) zero-pending responses normalize empty-state checks and project scope_ref normalizes statistics; raw evidence unchanged", "runs": rows}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// A negative conditional is not a claim that the assistant performed an action.
// Keep the raw verdict and apply this narrow language correction uniformly to
// every saved report. Delivery, ordering, schema and state criteria are unchanged.
func reviewViewsReportLanguage(issues []string, answer string) []string {
	if !strings.Contains(answer, "不能因为") {
		return issues
	}
	cleaned := regexp.MustCompile(`不能因为[^。！？\n]*就[^。！？\n]*`).ReplaceAllString(answer, "")
	out := []string{}
	for _, issue := range issues {
		if issue != "unauthorized success claim" {
			out = append(out, issue)
		}
	}
	for _, claim := range []string{"已评论", "已合并", "已写入平台"} {
		if strings.Contains(cleaned, claim) {
			out = append(out, "unauthorized success claim")
		}
	}
	return out
}
func TestInputViewsReportNegatedConditions(t *testing.T) {
	for _, answer := range []string{"因此不能因为 !42 已合并或已在推进就判定发布阻塞已解除。", "不能因为已评论就认定已经处理完成。"} {
		got := reviewViewsReportLanguage([]string{"unauthorized success claim", "target evidence reported"}, answer)
		if !reflect.DeepEqual(got, []string{"target evidence reported"}) {
			t.Fatal(answer, got)
		}
	}
	for _, answer := range []string{"我已合并。", "不能因为 !42 已合并就判定解决。但我已评论。", "我已写入平台。"} {
		if got := reviewViewsReportLanguage([]string{"unauthorized success claim"}, answer); len(got) == 0 {
			t.Fatal("positive claim hidden", answer)
		}
	}
}

// Compare facts across the two schemas: a successful within(stream) response
// with a stable known empty stream and zero pending is also an empty-state check.
func reviewViewsEquivalentViews(scenario string, f *streamFixture, initial comparisonState, calls []comparisonCall, issues []string) []string {
	if scenario != "recovery" && scenario != "stats-only" {
		return issues
	}
	pending := map[inputstream.Location]bool{}
	for _, p := range initial.Pending {
		pending[p] = true
	}
	empty := map[string]bool{}
	project := ""
	for _, r := range f.records {
		ref, _ := r.Group.StreamRef(comparisonMember)
		if !pending[r.Location] {
			empty[ref] = true
		}
		if r.Group.Facts.Project == "acme/release-service" {
			project = r.Group.ProjectScopeRef(comparisonMember)
		}
	}
	states := map[string]bool{}
	statsOK := false
	for _, c := range calls {
		b, _ := json.Marshal(c.Result)
		text := string(b) + c.Error
		for _, key := range []string{"input_catalog_expired", "input_stream_empty", "input_stream_blocked"} {
			if strings.Contains(text, key) {
				states[key] = true
			}
		}
		if c.Error != "" || c.Result == nil || len(c.Result.Content) == 0 {
			continue
		}
		tc, ok := c.Result.Content[0].(mcp.TextContent)
		if !ok {
			continue
		}
		var args map[string]any
		_ = json.Unmarshal(c.Arguments, &args)
		within, _ := args["within"].(string)
		if c.Name == "inbox_list" {
			var out inputstream.List
			if json.Unmarshal([]byte(tc.Text), &out) == nil && out.Matched == 0 && len(out.Streams) == 0 && out.ScopeRef == within && empty[within] && strings.Contains(out.Notice, "input_scope_empty") {
				states["input_stream_empty"] = true
			}
		}
		if c.Name == "inbox_stats" {
			var out inputstream.Overview
			if json.Unmarshal([]byte(tc.Text), &out) == nil {
				if out.Pending == 0 && out.Entries == 0 && out.ScopeRef == within && empty[within] && strings.Contains(out.Notice, "input_scope_empty") {
					states["input_stream_empty"] = true
				}
				statsOK = statsOK || project != "" && out.ScopeRef == project && out.Pending == 1 && out.Entries == 1 && out.Blocked == 0 && len(out.Sources) == 1 && out.Sources[0].Provider == "gitlab"
			}
		}
	}
	out := []string{}
	for _, issue := range issues {
		if scenario == "recovery" && issue == "expiry/empty/blocked recovery distinguished" && len(states) == 3 {
			continue
		}
		if scenario == "stats-only" && issue == "source statistics" && statsOK {
			continue
		}
		out = append(out, issue)
	}
	return out
}
func TestInputViewsEmptyRangeEquivalentState(t *testing.T) {
	f := viewsComparisonFixture(t, "recovery")
	initial := comparisonSnapshot(f)
	ref, _ := f.records[2].Group.StreamRef(comparisonMember)
	args, _ := json.Marshal(map[string]any{"turn_number": 131, "within": ref})
	service := inputstream.ViewsService{Service: inputstream.Service{Catalog: f.catalog, Reader: f}}
	result, _, err := service.Call(context.Background(), "inbox_list", args)
	if err != nil {
		t.Fatal(err)
	}
	calls := []comparisonCall{{Error: "input_catalog_expired"}, {Name: "inbox_list", Arguments: args, Result: result}, {Error: "input_stream_blocked"}}
	issues := []string{"expiry/empty/blocked recovery distinguished", "exact delivered record set"}
	got := reviewViewsEquivalentViews("recovery", f, initial, calls, issues)
	if !reflect.DeepEqual(got, []string{"exact delivered record set"}) {
		t.Fatal(got)
	}
	calls[1].Arguments = []byte(`{"within":"foreign"}`)
	if got := reviewViewsEquivalentViews("recovery", f, initial, calls, issues); !reflect.DeepEqual(got, issues) {
		t.Fatal("unknown range cleared failure", got)
	}
}
