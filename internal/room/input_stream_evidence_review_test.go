//go:build e2e && (darwin || linux)

package room

import (
	"encoding/json"
	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
	"os"
	"path/filepath"
	"testing"
)

// Offline replay only: preserve raw reports and their original judgments. No
// credentials, Session, model or platform calls. Every sample uses one oracle.
func TestInputDiscoveryEvidenceReview(t *testing.T) {
	root := os.Getenv("TEAMX_INPUT_COMPARISON_REVIEW")
	if root == "" {
		t.Skip("opt-in offline evidence review")
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", "evidence.json"))
	if err != nil || len(paths) == 0 {
		t.Fatal("evidence required", err)
	}
	type reviewRow struct {
		OriginalStatus string   `json:"original_status"`
		Status         string   `json:"status"`
		Issues         []string `json:"issues,omitempty"`
	}
	reviews := map[string]reviewRow{}
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
		if err = json.Unmarshal(data, &report); err != nil {
			t.Fatal(err)
		}
		f := comparisonFixture(t, report.Scenario)
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
		if report.Error != "" {
			issues = append(issues, "runtime error: "+report.Error)
		}
		row := reviewRow{OriginalStatus: report.Status, Status: "passed", Issues: issues}
		if len(issues) > 0 {
			row.Status = "failed"
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		reviews[relative] = row
	}
	output := os.Getenv("TEAMX_INPUT_COMPARISON_REVIEW_OUTPUT")
	if output == "" {
		t.Fatal("separate review output required")
	}
	data, err := json.MarshalIndent(map[string]any{"policy": "Original acceptance criteria, with Chinese/English explicit restriction recognition; no tool/input/state/schema changes", "runs": reviews}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(output, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
