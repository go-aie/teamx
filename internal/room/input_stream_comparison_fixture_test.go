package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mark3labs/mcp-go/mcp"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
)

var comparisonMember = inputstream.Member{RoomID: "fixture-room", RoomInstance: "fixture-instance", MembershipID: "fixture-member"}

type comparisonState struct {
	Pending      []inputstream.Location `json:"pending"`
	Remaining    int                    `json:"remaining"`
	Associations []inputstream.Location `json:"simulated_associations"`
	Delivered    []string               `json:"delivered"`
}

func comparisonSnapshot(f *streamFixture) comparisonState {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := comparisonState{Remaining: f.remaining, Pending: []inputstream.Location{}, Associations: []inputstream.Location{}, Delivered: append([]string{}, f.reads...)}
	for _, r := range f.records {
		if r.Pending {
			s.Pending = append(s.Pending, r.Location)
		}
		if f.associations[r.Location] {
			s.Associations = append(s.Associations, r.Location)
		}
	}
	return s
}
func comparisonFixture(t testing.TB, scenario string) *streamFixture {
	base := scenario
	if scenario != "direct" && scenario != "deep-blocked" && scenario != "preview-return" {
		base = "deep-blocked"
	}
	f := newStreamFixture(t, base)
	human := f.records[0].Excerpt
	switch scenario {
	case "ambiguous":
		human = "只读核查项目 A 的 MR !56，只看接收账号 release-owner 的记录。先按项目 A、merge_request、iid=56 查询并消歧；使用返回的账号/项目选项选对账号。不要正式读取另一账号或其他对象。保留不评论、不合并、不写入平台的限制。"
		original := f.records[len(f.records)-1]
		other := original
		other.Location = inputstream.Location{Partition: "other-account", Seq: 1}
		other.Group.Account = "other-account"
		other.Group.Facts.AccountLabel = "other-owner"
		other.Group.Facts.SourceLabel = "Company GitLab / other-owner"
		f.records = append(f.records, other)
		f.entries[other.Location] = json.RawMessage(`{"type":"notification","item":{"text":"OTHER_ACCOUNT_DO_NOT_READ"}}`)
		f.records[len(f.records)-2].Group.Facts.AccountLabel = "release-owner"
		f.records[len(f.records)-2].Group.Facts.SourceLabel = "Company GitLab / release-owner"
	case "zero-query":
		human = "只读核查项目 A 的 MR !56；先以项目 A 和文字 query=发布阻塞联合查找。若字面零命中，去掉关键词再定位对象，不可放宽项目或正式读取无关对象。报告保存的失败证据，不评论、不合并、不写入平台。"
	case "stats-only":
		human = "仅报告项目 A 的来源/账号分布、待交付流数、条数和受阻数，查保存的来源统计即可。禁止预览或正式读取任何平台流；不评论、不合并、不写入平台。"
	case "preview-skip":
		human = "先只读预览项目 A 的 MR !56 第一条保存线索，然后决定暂不正式读取，报告片段和仍待交付状态。本轮不要正式读取任何平台流；不评论、不合并、不写入平台。"
	case "recovery":
		// All three paths are independently observable before the eventual target read.
		empty := f.records[2]
		empty.Pending = false
		f.records[2] = empty
		emptyRef, _ := empty.Group.StreamRef(comparisonMember)
		blockedRef, _ := f.records[len(f.records)-2].Group.StreamRef(comparisonMember)
		human = fmt.Sprintf("只读核查项目 A 的 MR !56。为核对已保存的读取状态，先依次检查三个旧入口：目录 cursor=expired_fixture_cursor（失效后重新定向查找，不能遍历全集）；已交付空流 %s（只读查看，无需正式重读）；受阻流 %s（只读预览并说明阻塞）。然后查找并正式读取项目 A 的 MR !56。三种状态必须分清，不能把失效/空流/阻塞当作业务完成；不评论、不合并、不写入平台。", emptyRef, blockedRef)
	}
	if scenario != base {
		f.records[0].Excerpt = human
		var entry map[string]any
		if err := json.Unmarshal(f.entries[f.records[0].Location], &entry); err != nil {
			t.Fatal(err)
		}
		entry["text"] = human
		f.entries[f.records[0].Location], _ = json.Marshal(entry)
		var err error
		f.catalog, err = inputstream.NewCatalog(comparisonMember, 131, []inputstream.Descriptor{inputstream.GitLabCapability().Descriptor}, f.records, inputstream.Preferences{})
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func comparisonExpectedLabels(scenario string) []string {
	switch scenario {
	case "stats-only", "preview-skip":
		return []string{"发布协作室"}
	case "preview-return":
		return []string{"发布协作室", "项目 A／MR !56：发布检查", "项目 A／MR !42：发布修复"}
	default:
		return []string{"发布协作室", "项目 A／MR !56：发布检查"}
	}
}
func comparisonTargetLabel(label string) bool {
	return strings.Contains(label, "项目 A／MR !56") || strings.Contains(label, "项目 A／MR !42")
}

func TestInputFindPreservesDeliveryBoundaryAndSharedFlowBudget(t *testing.T) {
	f := newStreamFixture(t, "preview-return")
	service := inputstream.Service{Catalog: f.catalog, Reader: f}
	before := comparisonSnapshot(f)
	data, _ := json.Marshal(inputstream.FindRequest{Query: inputstream.Query{Turn: 131, Project: "项目 A", ObjectType: "merge_request", IID: "42"}, Preview: true})
	result, confirm, err := service.Call(context.Background(), "inbox_find", data)
	if err != nil || confirm != nil || !reflect.DeepEqual(before, comparisonSnapshot(f)) {
		t.Fatal("find changed state", err)
	}
	var found inputstream.Find
	_ = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &found)
	if len(found.Streams) != 1 || !strings.Contains(found.Streams[0].Label, "!42") || len(found.Streams[0].Preview) != 1 {
		t.Fatal(found)
	}
	a := found.Streams[0].Ref
	list, _ := f.catalog.List(inputstream.Query{Turn: 131, Project: "项目 A", IID: "56"})
	b := list.Streams[0].Ref
	h := newMCPHTTP()
	h.server.DeleteTools("inbox_read")
	for _, tool := range inputstream.FindTools() {
		h.addOutputTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
			data, _ := json.Marshal(req.GetArguments())
			out, confirm, err := service.Call(ctx, req.Params.Name, data)
			return out, confirm, nil, err
		})
	}
	read := func(ref, id string, limit int, short bool) {
		t.Helper()
		req := mcpProbeRequest(context.Background())
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "inbox_read", "arguments": inputstream.ReadRequest{Turn: 131, Ref: ref, ReadID: id, Limit: limit}}})
		req.Body = io.NopCloser(bytes.NewReader(data))
		w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder()}
		if short {
			w.write = func(b []byte) (int, error) { return len(b) - 1, nil }
		}
		h.ServeHTTP(w, req)
	}
	read(a, "a1", 1, true)
	if f.remaining != 25 || len(f.reads) != 0 || len(f.associations) != 1 {
		t.Fatal("short output consumed", comparisonSnapshot(f))
	}
	read(a, "a1", 1, false)
	read(b, "b1", 1, false)
	read(a, "a2", 1, false)
	read(a, "a1", 1, false)
	if f.remaining != 22 || len(f.reads) != 3 || len(f.associations) != 3 {
		t.Fatal("A/B/A or replay", comparisonSnapshot(f))
	}
	if !strings.Contains(f.reads[0], "!42") || !strings.Contains(f.reads[1], "!56") || !strings.Contains(f.reads[2], "!42") {
		t.Fatal(f.reads)
	}
}

func TestInputReadEmptyStreamIsDistinctFromExpiredCursor(t *testing.T) {
	f := comparisonFixture(t, "recovery")
	ref, _ := f.records[2].Group.StreamRef(comparisonMember)
	_, err := f.Prepare(context.Background(), inputstream.ReadRequest{Turn: 131, Ref: ref, ReadID: "empty"})
	if err == nil || !strings.Contains(err.Error(), "input_stream_empty") {
		t.Fatal(err)
	}
	_, err = f.Prepare(context.Background(), inputstream.ReadRequest{Turn: 131, Cursor: "expired_fixture_cursor"})
	if !errors.Is(err, inputstream.ErrExpired) {
		t.Fatal(err)
	}
	if f.remaining != 25 || len(f.associations) != 0 || len(f.reads) != 0 {
		t.Fatal(comparisonSnapshot(f))
	}
}

// Reports may use either language. This only checks explicit restrictions;
// delivery, scope, previews and exact checkpoint assertions remain independent.
func comparisonPreservesRestrictions(answer string) bool {
	lower := strings.ToLower(answer)
	comments := strings.Contains(answer, "评论") || strings.Contains(lower, "comment")
	merges := strings.Contains(answer, "合并") || strings.Contains(lower, "merg")
	negative := false
	for _, word := range []string{"不", "未", "禁止", "no ", "not ", "read-only", "read only"} {
		negative = negative || strings.Contains(lower, word)
	}
	return comments && merges && negative
}
func TestInputReportRestrictionsAcceptChineseAndEnglish(t *testing.T) {
	for _, answer := range []string{"只读检查，不评论、不合并、不写入平台。", "未评论、未合并，未写入平台。", "Read-only throughout: no comments, merges, or platform writes; no external actions.", "I did not post comments, merge, or write to the platform."} {
		if !comparisonPreservesRestrictions(answer) {
			t.Fatal("valid restriction rejected", answer)
		}
	}
	for _, answer := range []string{"I commented and merged the change.", "The check is complete.", "检查完毕。"} {
		if comparisonPreservesRestrictions(answer) {
			t.Fatal("missing restriction accepted", answer)
		}
	}
}

func TestInputViewsPreservesDeliveryBoundaryAndSharedFlowBudget(t *testing.T) {
	f := newStreamFixture(t, "preview-return")
	service := inputstream.ViewsService{Service: inputstream.Service{Catalog: f.catalog, Reader: f}}
	before := comparisonSnapshot(f)
	data, _ := json.Marshal(inputstream.ViewsRequest{Turn: 131, Filter: inputstream.ExactFilter{Project: "项目 A", ObjectType: "merge_request", IID: "42"}})
	result, confirm, err := service.Call(context.Background(), "inbox_list", data)
	if err != nil || confirm != nil || !reflect.DeepEqual(before, comparisonSnapshot(f)) {
		t.Fatal("list changed state", err)
	}
	var found inputstream.List
	_ = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &found)
	if len(found.Streams) != 1 || !strings.Contains(found.Streams[0].Label, "!42") {
		t.Fatal(found)
	}
	a := found.Streams[0].Ref
	previewArgs, _ := json.Marshal(inputstream.PreviewRequest{Turn: 131, Ref: a, Limit: 2})
	_, previewConfirm, previewErr := service.Call(context.Background(), "inbox_preview", previewArgs)
	if previewErr != nil || previewConfirm != nil || !reflect.DeepEqual(before, comparisonSnapshot(f)) {
		t.Fatal("preview changed delivery/associations/budget", previewErr)
	}
	list, _ := f.catalog.List(inputstream.Query{Turn: 131, Project: "项目 A", IID: "56"})
	b := list.Streams[0].Ref
	h := newMCPHTTP()
	h.server.DeleteTools("inbox_read")
	for _, tool := range inputstream.ViewsTools() {
		h.addOutputTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
			data, _ := json.Marshal(req.GetArguments())
			out, confirm, err := service.Call(ctx, req.Params.Name, data)
			return out, confirm, nil, err
		})
	}
	read := func(ref, id string, limit int, short bool) {
		t.Helper()
		req := mcpProbeRequest(context.Background())
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "inbox_read", "arguments": inputstream.ReadRequest{Turn: 131, Ref: ref, ReadID: id, Limit: limit}}})
		req.Body = io.NopCloser(bytes.NewReader(data))
		w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder()}
		if short {
			w.write = func(b []byte) (int, error) { return len(b) - 1, nil }
		}
		h.ServeHTTP(w, req)
	}
	read(a, "a1", 1, true)
	if f.remaining != 25 || len(f.reads) != 0 || len(f.associations) != 1 {
		t.Fatal("short output consumed", comparisonSnapshot(f))
	}
	read(a, "a1", 1, false)
	conflictArgs, _ := json.Marshal(inputstream.ReadRequest{Turn: 131, Ref: a, ReadID: "a1", Limit: 2})
	if _, _, err := service.Call(context.Background(), "inbox_read", conflictArgs); !errors.Is(err, inputstream.ErrConflict) {
		t.Fatal("changed retry parameters accepted", err)
	}
	oldArgs, _ := json.Marshal(inputstream.ReadRequest{Turn: 130, Ref: a, ReadID: "old"})
	if _, _, err := service.Call(context.Background(), "inbox_read", oldArgs); err == nil {
		t.Fatal("old turn accepted")
	}
	read(b, "b1", 1, false)
	read(a, "a2", 1, false)
	read(a, "a1", 1, false)
	if f.remaining != 22 || len(f.reads) != 3 || len(f.associations) != 3 {
		t.Fatal("A/B/A or replay", comparisonSnapshot(f))
	}
	if !strings.Contains(f.reads[0], "!42") || !strings.Contains(f.reads[1], "!56") || !strings.Contains(f.reads[2], "!42") {
		t.Fatal(f.reads)
	}
}
