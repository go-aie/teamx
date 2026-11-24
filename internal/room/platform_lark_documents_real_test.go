//go:build e2e

package room

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Uses one operator-selected existing account/document through the production
// TeamX tools CLI and HTTP path. No model, document creation or OAuth grant.
func TestRealLarkDocumentTool(t *testing.T) {
	documentURL := os.Getenv("TEAMX_LARK_TEST_DOC_URL")
	profile := os.Getenv("TEAMX_LARK_TEST_PROFILE")
	if documentURL == "" || profile == "" {
		t.Skip("set TEAMX_LARK_TEST_DOC_URL and TEAMX_LARK_TEST_PROFILE for a permitted read-only sample")
	}
	m, room, svc, members := toolRoomFixture(t, 1)
	m.toolCatalog = append(m.toolCatalog, tool.Definition{Name: "lark", Provider: "lark", InternalOnly: true, Skill: "skills/lark/SKILL.md"})
	brand := os.Getenv("TEAMX_LARK_TEST_BRAND")
	if brand == "" {
		brand = "feishu"
	}
	p := platform.CLIProfile{Version: 1, Executable: os.Getenv("TEAMX_LARK_TEST_BIN"), Profile: profile, Brand: brand, CLIVersion: lark.CLIVersion}
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "文档只读验收", Provider: "lark", URL: lark.Origin(brand), CLIProfile: &p})
	c, err := m.verifyPlatform(t.Context(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" {
		t.Fatal("account verification:", err)
	}
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "lark", 0)
	client, endpoint := toolPipeServer(t, m)
	var stdout, stderr bytes.Buffer
	err = runToolsWithClient(t.Context(), []string{"run", c.ToolEntryID, "docs", "fetch", "--url", documentURL}, bytes.NewReader(nil), &stdout, &stderr, cliToolEnv(endpoint, svc, members[0]), client)
	var result lark.DocumentResult
	if err != nil {
		t.Fatal("document tool:", err)
	}
	var body struct {
		Document struct {
			ID      string `json:"document_id"`
			Content string `json:"content"`
		} `json:"document"`
	}
	if json.Unmarshal(stdout.Bytes(), &result) != nil || json.Unmarshal(result.Data, &body) != nil || body.Document.ID == "" || body.Document.Content == "" || !result.Complete {
		t.Fatal("expected nonempty complete document")
	}
	// A wiki's returned native document ID lets us verify the same document's
	// direct docx route, without discovering or reading a second document.
	directDocx := false
	parsed, err := url.Parse(documentURL)
	if err != nil {
		t.Fatal("invalid test URL")
	}
	if strings.HasPrefix(parsed.Path, "/wiki/") && body.Document.ID != strings.TrimPrefix(parsed.Path, "/wiki/") {
		parsed.Path, parsed.RawQuery, parsed.Fragment = "/docx/"+body.Document.ID, "", ""
		stdout.Reset()
		stderr.Reset()
		if err := runToolsWithClient(t.Context(), []string{"run", c.ToolEntryID, "docs", "fetch", "--url", parsed.String()}, bytes.NewReader(nil), &stdout, &stderr, cliToolEnv(endpoint, svc, members[0]), client); err != nil {
			t.Fatal("same document via docx:", err)
		}
		var direct lark.DocumentResult
		var content struct {
			Document struct {
				ID      string `json:"document_id"`
				Content string `json:"content"`
			} `json:"document"`
		}
		if json.Unmarshal(stdout.Bytes(), &direct) != nil || json.Unmarshal(direct.Data, &content) != nil || !direct.Complete || content.Document.ID != body.Document.ID || content.Document.Content != body.Document.Content {
			t.Fatal("wiki and direct docx did not return the same complete document")
		}
		directDocx = true
	}
	if m.platformCalls.Len() != 0 {
		t.Fatal("real document call did not settle")
	}
	t.Logf("TeamX CLI/HTTP + real lark-cli: content_bytes=%d complete=%t same_document_docx=%t native_writes=0", len(body.Document.Content), result.Complete, directDocx)
}
