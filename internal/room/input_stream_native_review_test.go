//go:build e2e && (darwin || linux)

package room

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	old "github.com/go-aie/teamx/internal/inputstreamtest"
)

// Re-evaluate every raw trial with one semantic oracle. Preserve original
// judgments and hashes; this never opens a Session or makes external calls.
func TestInputNativeEvidenceReview(t *testing.T) {
	root := os.Getenv("TEAMX_INPUT_NATIVE_REVIEW")
	if root == "" {
		t.Skip("opt-in offline evidence review")
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", "evidence.json"))
	if err != nil || len(paths) == 0 {
		t.Fatal("evidence required", err)
	}
	reviews := map[string]any{}
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
		if report.Scenario == "ext-domain" {
			f = domainComparisonFixture(t)
		}
		nativeFixtureTask(f)
		if report.Mode == "native" {
			if _, err := newNativeComparison(t, f).catalog(); err != nil {
				t.Fatal(err)
			}
		}
		pending := map[old.Location]bool{}
		for _, l := range report.Final.Pending {
			pending[l] = true
		}
		for i := range f.records {
			f.records[i].Pending = pending[f.records[i].Location]
		}
		f.remaining = report.Final.Remaining
		f.reads = report.Final.Delivered
		for _, l := range report.Final.Associations {
			f.associations[l] = true
		}
		issues := validateNativeComparison(report.Scenario, report.Mode, f, report.Initial, report.Calls, report.Answer)
		if report.Error != "" {
			issues = append(issues, "runtime error: "+report.Error)
		}
		status := "passed"
		if len(issues) > 0 {
			status = "failed"
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		reviews[rel] = map[string]any{"original_status": report.Status, "status": status, "issues": issues, "evidence_sha256": fmt.Sprintf("%x", sha256.Sum256(data))}
	}
	output := os.Getenv("TEAMX_INPUT_NATIVE_REVIEW_OUTPUT")
	if output == "" {
		t.Fatal("separate output required")
	}
	data, err := json.MarshalIndent(map[string]any{"policy": "Exact saved state and scope: project overview must produce the reused native project scope; native and text predicates must intersect; state probes preserve order; positive write claims reject while negation is accepted. Same oracle for all raw samples; no evidence overwritten.", "runs": reviews}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
