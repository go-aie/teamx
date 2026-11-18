package room

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/inputstream"
	frozen "github.com/go-aie/teamx/internal/inputstreamtest/frozen"
)

func TestNativeDomainFixtureUsesPrivateFilterAndWholeStream(t *testing.T) {
	f := domainComparisonFixture(t)
	n := newNativeComparison(t, f)
	c, err := n.catalog()
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.List(frozen.Query{Text: "采购审批", Kind: "notification"})
	if err != nil || list.Matched != 3 || list.ScopeRef == "" {
		t.Fatal(list, err)
	}
	stats, err := c.Stats(frozen.StatsQuery{Within: list.ScopeRef})
	if err != nil || stats.Guidance == nil || len(stats.Guidance.Text) > 1024 {
		t.Fatal(stats, err)
	}
	q := frozen.Query{Within: list.ScopeRef, Filter: json.RawMessage(`{"node":"finance","status":"pending"}`)}
	selected, err := c.List(q)
	if err != nil || selected.Matched != 1 || selected.Streams[0].Pending != 2 {
		t.Fatal(selected, err)
	}
	body, _ := json.Marshal(inputstream.ReadRequest{Turn: 131, Stream: selected.Streams[0].Ref, ID: "domain"})
	result, confirm, err := n.Call(context.Background(), "inbox_read", body)
	if err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(result)
	if !strings.Contains(string(serialized), "FIRST_APPROVAL_CONTEXT") || !strings.Contains(string(serialized), "APPROVAL_REVIEW_PENDING") {
		t.Fatal(string(serialized))
	}
	if err := confirm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.remaining != 23 {
		t.Fatal(f.remaining)
	}
}
