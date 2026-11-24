package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
)

// DocumentError is read-only repair guidance for this account. Document access
// failures must not invalidate an otherwise usable messaging connection.
type DocumentError struct {
	Code       string      `json:"code"`
	NextAction string      `json:"next_action"`
	Hint       string      `json:"hint"`
	Capability *Capability `json:"capability,omitempty"`
}

func (e *DocumentError) Error() string { return "lark document: " + e.Code }
func (e *DocumentError) Unwrap() error { return failure(e.Code) }

// Data retains the CLI's entire document response, including reference_map,
// comments and degradation tips. No embedded resource is fetched separately.
type DocumentResult struct {
	URL      string          `json:"url"`
	Format   string          `json:"format"`
	Complete bool            `json:"complete"`
	Data     json.RawMessage `json:"data"`
}

// NormalizeDocumentURL accepts document links by path, including alternate
// Feishu domains. The CLI uses the token with its bound platform API, never an
// HTTP fetch of this host. Remove sharing queries and selection anchors: this
// command always requests the full document, not an anchored excerpt.
func NormalizeDocumentURL(raw string) (string, error) {
	if len(raw) > 4096 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\r\n\t") {
		return "", failure("unsupported_document_url")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Port() != "" && u.Port() != "443" {
		return "", failure("unsupported_document_url")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "docx" && parts[0] != "wiki" || !documentToken(parts[1]) || u.RawPath != "" {
		return "", failure("unsupported_document_url")
	}
	u.Path = "/" + parts[0] + "/" + parts[1]
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	u.ForceQuery = false
	return u.String(), nil
}

func documentToken(token string) bool {
	return len(token) > 0 && len(token) <= 256 && strings.Trim(token, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == ""
}

func (c *Client) FetchDocument(ctx context.Context, account platform.AccountIdentity, rawURL string) (DocumentResult, error) {
	docURL, err := NormalizeDocumentURL(rawURL)
	if err != nil {
		return DocumentResult{}, &DocumentError{Code: "unsupported_document_url", NextAction: "check_document_url", Hint: "Use an HTTPS /docx/TOKEN or /wiki/TOKEN document URL."}
	}
	var result DocumentResult
	err = c.guarded(ctx, account, func(ctx context.Context) error {
		app, user := c.inspectScopes(ctx, account, strings.Fields(DocumentReadScopes))
		if app.ReasonCode == "identity_mismatch" {
			return failure("identity_mismatch")
		}
		cap := permissionCapabilities(PermissionCheck{Application: app, User: user}, "", "")
		for _, v := range cap {
			if v.Name == "document.read" && v.State != "ready" {
				return &DocumentError{Code: "document_" + v.ReasonCode, NextAction: v.NextAction, Hint: "Repair document.read on this same connection; messaging permissions are independent.", Capability: &v}
			}
		}
		// Fixed read-only shortcut/flags: no Agent-provided CLI arguments, files,
		// alternate profile, output format or resource-download operation.
		var data json.RawMessage
		if err := c.apiJSON(ctx, []string{"docs", "+fetch", "--doc", docURL, "--doc-format", "markdown", "--detail", "simple", "--scope", "full"}, &data); err != nil {
			if ErrorCode(err) == "permission_denied" {
				return &DocumentError{Code: "document_access_denied", NextAction: "check_document_access", Hint: "Document scopes were checked. Verify this account's access to the document and its wiki space; do not switch accounts."}
			}
			if ErrorCode(err) == "query_failed" {
				return &DocumentError{Code: "document_unavailable", NextAction: "check_document_access", Hint: "The document could not be read on this account. Check its URL and access; missing documents and denied access may be indistinguishable upstream."}
			}
			return err
		}
		result, err = decodeDocumentResult(docURL, data)
		return err
	})
	if err != nil {
		return DocumentResult{}, err
	}
	return result, nil
}

func decodeDocumentResult(docURL string, data json.RawMessage) (DocumentResult, error) {
	var wire struct {
		Document *struct {
			ID        string          `json:"document_id"`
			Content   *string         `json:"content"`
			Tips      json.RawMessage `json:"tips"`
			Truncated bool            `json:"truncated"`
			HasMore   bool            `json:"has_more"`
		} `json:"document"`
		Tips      json.RawMessage `json:"tips"`
		Warnings  json.RawMessage `json:"warnings"`
		Truncated bool            `json:"truncated"`
		HasMore   bool            `json:"has_more"`
	}
	if decode(data, &wire) != nil || wire.Document == nil || !documentToken(wire.Document.ID) || wire.Document.Content == nil {
		return DocumentResult{}, failure("invalid_response")
	}
	u, _ := url.Parse(docURL)
	if strings.HasPrefix(u.Path, "/docx/") && strings.TrimPrefix(u.Path, "/docx/") != wire.Document.ID {
		return DocumentResult{}, failure("invalid_response")
	}
	complete := !wire.Truncated && !wire.HasMore && !wire.Document.Truncated && !wire.Document.HasMore && !documentNotice(wire.Tips) && !documentNotice(wire.Warnings) && !documentNotice(wire.Document.Tips) && !strings.HasPrefix(strings.TrimSpace(*wire.Document.Content), "<fragment")
	return DocumentResult{URL: docURL, Format: "markdown", Complete: complete, Data: data}, nil
}

func documentNotice(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", `""`, "[]", "{}", "false":
		return false
	default:
		return true
	}
}
