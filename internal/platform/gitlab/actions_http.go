package gitlab

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// ErrNoteNotSent means admission was revoked before invoking the write transport.
var ErrNoteNotSent = errors.New("note stopped before sending")

// NoteAPI is deliberately limited to one MR note or one native thread reply.
// It is only injected into registered action capabilities, never notification GET
// or an Agent-facing arbitrary method/URL entry point.
type NoteAPI func(ctx context.Context, projectID, mrIID, discussionID, body string) (APIResponse, error)
type noteError struct {
	message               string
	retryable, notApplied bool
}

func (e *noteError) Error() string { return e.message }

// NewNoteAPI shares the read boundary's origin, credential, redirect, timeout,
// size and redaction rules. There is no business POST retry or redirect replay.
func NewNoteAPI(origin string, secret platform.Secret, transport http.RoundTripper) NoteAPI {
	return NewNoteAPIWithCredential(origin, platform.AccessCredential{Kind: "oauth", Token: secret}, transport)
}

func NewNoteAPIWithCredential(origin string, credential platform.AccessCredential, transport http.RoundTripper) NoteAPI {
	secret := credential.Token
	client := boundedHTTPClient(transport)
	return func(ctx context.Context, projectID, mrIID, discussionID, body string) (APIResponse, error) {
		before := func() (APIResponse, error) {
			return APIResponse{}, &noteError{message: "request stopped before sending", notApplied: true}
		}
		if ctx.Err() != nil || !validOrigin(origin) || !validToken(secret.Reveal()) || !positiveREST(platform.ExternalID{Namespace: "gitlab:rest:id", Value: projectID}, "id") || !positiveREST(platform.ExternalID{Namespace: "gitlab:rest:iid", Value: mrIID}, "iid") || !actionText(body) || !validActionDiscussion(discussionID) {
			return before()
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		path := "/projects/" + projectID + "/merge_requests/" + mrIID
		if discussionID != "" {
			path += "/discussions/" + url.PathEscape(discussionID)
		}
		path += "/notes"
		data, _ := json.Marshal(map[string]string{"body": body})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/v4"+path, bytes.NewReader(data))
		if err != nil {
			return before()
		}
		req.GetBody = nil // no replay, including 307/308 redirects
		if setAuthentication(req, origin, credential) != nil {
			return before()
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			var unknownCA x509.UnknownAuthorityError
			var invalidCert x509.CertificateInvalidError
			var hostname x509.HostnameError
			retry := ctx.Err() == nil && !errors.As(err, &unknownCA) && !errors.As(err, &invalidCert) && !errors.As(err, &hostname)
			return APIResponse{}, &noteError{message: "request interrupted or transport failed; remote result unconfirmed", retryable: retry}
		}
		defer resp.Body.Close()
		status := resp.StatusCode
		if status < 200 || status >= 300 {
			return APIResponse{}, &noteError{message: fmt.Sprintf("GitLab API HTTP %d (no automatic retry)", status), retryable: status == 408 || status == 429 || status >= 500, notApplied: status >= 400 && status < 500 && status != 408}
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil {
			return APIResponse{}, &noteError{message: "API response interrupted; remote result unconfirmed", retryable: ctx.Err() == nil}
		}
		if len(raw) > 1<<20 {
			return APIResponse{}, &noteError{message: "incomplete or invalid API response; remote result unconfirmed"}
		}
		data, err = safeAPIJSON(raw, secret)
		if err != nil {
			return APIResponse{}, &noteError{message: "incomplete or invalid API response; remote result unconfirmed"}
		}
		// Do not discard a complete response merely because revocation raced with
		// reading/closing it. The adapter verifies it and Room settles the old account.
		return APIResponse{Data: data, Status: status, Bytes: int64(len(raw))}, nil
	}
}
