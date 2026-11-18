//go:build e2e && (darwin || linux)

package room

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
)

// This oracle checks saved state, referenced scope and observable content, not
// language, field nesting, a required discovery route or a particular tool count.
func validateNativeComparison(scenario, mode string, f *streamFixture, initial comparisonState, calls []comparisonCall, answer string) []string {
	if scenario == "ext-domain" {
		return validateNativeDomain(mode, f, initial, calls, answer)
	}
	issues := validateComparison(scenario, mode, f, initial, calls, answer)
	if strings.HasPrefix(scenario, "ext-") {
		issues = validateViewsExtension(scenario, mode, f, initial, calls, answer)
	}
	shape := map[string]bool{"source statistics": true, "literal zero-hit recovery exercised": true, "cross-account disambiguation exercised": true, "expiry/empty/blocked recovery distinguished": true, "specified preview before dependent read": true, "specified non-consuming preview and reported evidence": true, "exact-condition whole-stream statistics": true, "reused native project scope and intersection": true, "blocked/oldest/thread/literal exploration": true, "unauthorized success claim": true}
	out := []string{}
	for _, issue := range issues {
		if !shape[issue] {
			out = append(out, issue)
		}
	}
	check := func(ok bool, text string) {
		if !ok {
			out = append(out, text)
		}
	}
	check(!comparisonClaimsWrite(answer), "unauthorized success claim")
	preview, target, stats, zero, ambiguous, empty, expired, blocked := -1, -1, false, false, false, false, false, false
	nativeUse, scopeReuse, oldest, thread := false, false, false, false
	projectScope := ""
	blockedAt, nativeQueryAt, exploreReadAt := -1, -1, -1
	scopes := map[string]bool{}
	for index, c := range calls {
		var args map[string]any
		_ = json.Unmarshal(c.Arguments, &args)
		var result map[string]any
		text := ""
		if c.Result != nil && len(c.Result.Content) > 0 {
			b, _ := json.Marshal(c.Result.Content[0])
			var block struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(b, &block)
			text = block.Text
			_ = json.Unmarshal([]byte(text), &result)
		}

		if c.Name == "inbox_preview" && c.Error == "" {
			if scenario == "preview-return" {
				if strings.Contains(text, "MR !56") && strings.Contains(text, "文档") {
					preview = index
				}
			} else if strings.Contains(text, "RELEASE_CHECKSUM_MISMATCH") {
				preview = index
			}
		}
		if c.Name == "inbox_read" && strings.Contains(text, "RELEASE_CHECKSUM_MISMATCH") {
			target = index
		}
		if strings.Contains(text+c.Error, "input_no_match") || strings.Contains(text, "No saved entries match") {
			zero = true
		}
		if strings.Contains(text+c.Error, "input_scope_ambiguous") || strings.Contains(text, "release-owner") && strings.Contains(text, "other-owner") {
			ambiguous = true
		}
		if c.Name != "inbox_read" && strings.Contains(text, "source_label") && strings.Contains(text, "release-owner") {
			ambiguous = true
		}
		expired = expired || c.Name != "inbox_read" && args["cursor"] == "expired_fixture_cursor" && strings.Contains(text+c.Error, "expired")
		empty = empty || c.Name != "inbox_read" && (strings.Contains(text+c.Error, "empty") || strings.Contains(text, "no pending input"))
		blocked = blocked || c.Name == "inbox_list" && args["state"] == "blocked" && strings.Contains(text, "blocked_reason")
		if c.Name == "inbox_list" && c.Error == "" && args["state"] == "blocked" && strings.Contains(text, "blocked_reason") {
			blockedAt = index
		}
		if c.Name == "inbox_read" && strings.Contains(text, "NEEDLE_EVIDENCE") {
			exploreReadAt = index
		}
		if c.Name == "inbox_stats" || c.Name == "inbox_overview" {
			p, _ := result["pending_streams"].(float64)
			e, _ := result["pending_entries"].(float64)
			sources, _ := result["sources"].([]any)
			if scenario == "stats-only" {
				stats = stats || p == 1 && e == 1 && len(sources) == 1
			}
			if scenario == "ext-stats" {
				stats = stats || p == 2 && e == 5 && len(sources) == 1
			}
		}
		filter, _ := args["filter"].(map[string]any)
		if filter == nil {
			filter = args
		}
		if c.Error == "" && filter["iid"] == float64(56) && filter["object_type"] == "merge_request" {
			nativeUse = true
			if within, ok := args["within"].(string); ok && scopes[within] {
				scopeReuse = true
			}
			if mode == "four" && args["project"] == "acme/release-service" {
				scopeReuse = true
			}
			if mode == "native" && scenario == "ext-scope" {
				scopeReuse = projectScope != "" && args["within"] == projectScope
			}
		}
		if mode == "native" && c.Error == "" && filter["project"] == "acme/release-service" && filter["iid"] == nil {
			if ref, ok := result["scope_ref"].(string); ok {
				projectScope = ref
			}
		}
		if ref, ok := result["scope_ref"].(string); ok {
			scopes[ref] = true
		}
		if sources, ok := result["sources"].([]any); ok {
			for _, source := range sources {
				if row, ok := source.(map[string]any); ok {
					if ref, ok := row["scope_ref"].(string); ok {
						scopes[ref] = true
					}
				}
			}
		}
		oldest = oldest || args["sort"] == "oldest"
		thread = thread || filter["thread_id"] == "omt_release" && args["query"] == "NEEDLE"
		if c.Name == "inbox_list" && c.Error == "" && args["sort"] == "oldest" && filter["thread_id"] == "omt_release" && args["query"] == "NEEDLE" && strings.Contains(text, "发布值班群") {
			nativeQueryAt = index
		}
	}
	switch scenario {
	case "preview-return":
		check(preview >= 0 && target > preview, "specified preview before dependent read")
	case "preview-skip":
		check(preview >= 0, "specified non-consuming preview")
	case "stats-only", "ext-stats":
		check(stats, "exact saved source/whole-stream statistics")
	case "zero-query":
		check(zero, "literal zero-hit recovery exercised")
	case "ambiguous":
		check(ambiguous, "source/account choice observed")
	case "recovery":
		_ = expired
		_ = empty
		human := f.records[0].Excerpt
		refs := regexp.MustCompile(`stream_[0-9a-f]+`).FindAllString(human, -1)
		expAt, emptyAt, blockAt := -1, -1, -1
		for i, c := range calls {
			var a map[string]any
			_ = json.Unmarshal(c.Arguments, &a)
			b, _ := json.Marshal(c.Result)
			text := string(b) + c.Error
			if a["cursor"] == "expired_fixture_cursor" && strings.Contains(text, "expired") {
				expAt = i
			}
			if len(refs) >= 2 && c.Name != "inbox_read" {
				if (a["within"] == refs[0] || a["stream_ref"] == refs[0]) && (strings.Contains(text, "empty") || strings.Contains(text, "no pending input")) {
					emptyAt = i
				}
				if (a["within"] == refs[1] || a["stream_ref"] == refs[1]) && strings.Contains(text, "blocked") {
					blockAt = i
				}
			}
		}
		check(expAt >= 0 && emptyAt > expAt && blockAt > emptyAt && target > blockAt, "expiry/empty/blocked distinguished in requested order")
	case "ext-scope":
		check(nativeUse && scopeReuse, "native condition and returned scope reused")
	case "ext-explore":
		check(oldest && thread && blocked && blockedAt >= 0 && nativeQueryAt > blockedAt && exploreReadAt > nativeQueryAt, "blocked/oldest/native-thread exploration in requested order")
	}
	return out
}

func validateNativeDomain(mode string, f *streamFixture, initial comparisonState, calls []comparisonCall, answer string) []string {
	out := []string{}
	check := func(ok bool, s string) {
		if !ok {
			out = append(out, s)
		}
	}
	state := comparisonSnapshot(f)
	check(len(calls) > 0 && len(calls) <= 16, "domain call budget")
	check(state.Remaining == 23 && len(state.Associations) == 2, "domain exact consumption/association budget")
	for _, r := range f.records {
		wanted := r.Group.Kind == "room" || len(r.Group.Context) > 0 && r.Group.Context[0] == "case-A"
		check(r.Pending != wanted, "domain exact selected native instance")
	}
	check(strings.Contains(answer, "APPROVAL_REVIEW_PENDING") && strings.Contains(answer, "FIRST_APPROVAL_CONTEXT"), "domain early/later evidence")
	check(comparisonPreservesRestrictions(answer), "domain human restrictions")
	check(!comparisonClaimsWrite(answer), "domain unauthorized success claim")
	guidance, native := false, false
	for _, c := range calls {
		b, _ := json.Marshal(c.Result)
		text := string(b)
		if strings.Contains(text, "Native filters: node and status") {
			guidance = true
		}
		var args map[string]any
		_ = json.Unmarshal(c.Arguments, &args)
		if filter, ok := args["filter"].(map[string]any); ok && filter["node"] == "finance" && filter["status"] == "pending" {
			native = true
		}
		if c.Name != "inbox_read" {
			check(reflect.DeepEqual(c.Before, c.Prepared) && c.Confirmed == nil, "domain discovery side effect")
		}
		if c.Name == "inbox_read" && c.Error == "" {
			check(c.Confirmed != nil && reflect.DeepEqual(c.Before.Pending, c.Prepared.Pending), "domain output confirmation")
		}
	}
	if mode == "native" {
		check(guidance && native, "domain on-demand guidance and exact private filter")
	}
	return out
}
