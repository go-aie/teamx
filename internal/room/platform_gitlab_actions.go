package room

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func (m *roomManager) prepareGitlabReferenceAction(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
	return m.prepareGitlabActionSession(ctx, id, old, false)
}

func (m *roomManager) resolveGitlabActionTarget(ctx context.Context, id MemberIdentity, locator platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
	if _, ok := ctx.Value(toolEntryContextKey{}).(toolEntryContext); !ok {
		return platform.TargetResolution{}, referenceActionSession{}, invalidField("tool", "select an exact GitLab entry from teamx tools list")
	}
	session, err := m.prepareGitlabActionSession(ctx, id, platform.AccessContext{}, true)
	if err != nil {
		return platform.TargetResolution{}, session, err
	}
	r, err := session.Capabilities.Resolve(ctx, locator, session.Access)
	return r, session, err
}

func (m *roomManager) prepareGitlabActionSession(ctx context.Context, id MemberIdentity, old platform.AccessContext, standalone bool) (referenceActionSession, error) {
	executionInput := ""
	hasImages := false
	if call, ok := ctx.Value(roomActionContextKey{}).(roomActionContext); ok {
		executionInput = call.Input
		if err := decodeRequestJSON(executionInput, &gitlab.ImageActionInput{}); err != nil {
			return referenceActionSession{}, prefixDiagnostic(err, "input.")
		}
		input, err := gitlab.DecodeImageActionInput([]byte(executionInput))
		if err != nil {
			return referenceActionSession{}, gitlabImageInputDiagnostic(err)
		}
		hasImages = len(input.Images) > 0
	}
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	if svc == nil {
		m.mu.Unlock()
		return referenceActionSession{}, ErrIdentity
	}
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), Tool: "gitlab", platformID: old.PlatformID}
	admission, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, false)
	m.mu.Unlock()
	if err != nil {
		return referenceActionSession{}, err
	}
	cancelContent := func() {}
	if hasImages {
		callCtx, cancelContent = context.WithTimeout(callCtx, 150*time.Second)
	}
	closeCall := func() { cancelContent(); m.finishPlatformCall(call) }
	receiver := old.Receiver.ID
	if standalone {
		receiver = gitlabReferenceAccess(id, admission.account, platform.ExternalID{}).Account.ID
	}
	access := gitlabReferenceAccess(id, admission.account, receiver)
	commitCheck := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return m.checkGitlabReferenceAccessLocked(callCtx, req, access)
	}
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		return commitCheck(ctx)
	}
	requestContext := func(ctx context.Context) (context.Context, func()) {
		// Parent directly to the admitted call so binding revocation propagates
		// synchronously. Preserve the capability's shorter overall deadline.
		var merged context.Context
		var cancel context.CancelFunc
		if deadline, ok := ctx.Deadline(); ok {
			merged, cancel = context.WithDeadline(callCtx, deadline)
		} else {
			merged, cancel = context.WithCancel(callCtx)
		}
		if final, ok := ctx.Value(actionDispatchCheckKey{}).(actionDispatchCheck); ok {
			merged = context.WithValue(merged, actionDispatchCheckKey{}, final)
		}
		stop := context.AfterFunc(ctx, cancel)
		if ctx.Err() != nil {
			cancel()
		}
		return merged, func() { stop(); cancel() }
	}
	read := m.gitlabAccountRead(id.RoomIdentity, admission.account, admission.secret)
	guardedRead := func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (gitlab.APIResponse, error) {
		if err := check(ctx); err != nil {
			return gitlab.APIResponse{}, err
		}
		ctx, finish := requestContext(ctx)
		defer finish()
		response, err := read(ctx, path, q, h, limit)
		if currentErr := check(ctx); currentErr != nil {
			return gitlab.APIResponse{}, currentErr
		}
		return response, err
	}
	guardedWrite := func(ctx context.Context, project, iid, discussion, body string) (gitlab.APIResponse, error) {
		if check(ctx) != nil {
			return gitlab.APIResponse{}, gitlab.ErrNoteNotSent
		}
		ctx, finish := requestContext(ctx)
		defer finish()
		// Never discard a known write outcome after revocation. Further requests
		// still pass the guard; the original action retains the admitted account.
		useCtx, credential, release, err := m.acquireGitLabAccess(ctx, id.RoomIdentity, admission.account, admission.secret, 30*time.Second)
		if err != nil {
			return gitlab.APIResponse{}, gitlab.ErrNoteNotSent
		}
		response, writeErr := gitlab.NewNoteAPIWithCredential(admission.account.URL, credential, m.platformHTTP)(useCtx, project, iid, discussion, body)
		_ = release() // preserve the known write outcome even when authorization changed
		return response, writeErr
	}
	caps := platform.ReferenceCapabilities{Resolve: func(ctx context.Context, l platform.TargetLocator, a platform.AccessContext) (platform.TargetResolution, error) {
		if standalone {
			return gitlab.ResolveActionURL(ctx, l, a, guardedRead, admission.account.Username)
		}
		return gitlab.ResolveStoredActionTarget(ctx, l, a, guardedRead, admission.account.Username)
	}, CredentialKinds: []string{"gitlab:oauth"}}
	var lease *roomActionImages
	var leaseMu sync.Mutex
	var selected gitlab.ImageActionInput
	closeSession := func() {
		leaseMu.Lock()
		if lease != nil {
			lease.Close()
		}
		leaseMu.Unlock()
		closeCall()
	}
	source := func(ctx context.Context, input gitlab.ImageActionInput) (gitlab.PreparedImageComment, error) {
		ctx, finish := requestContext(ctx)
		defer finish()
		leaseMu.Lock()
		defer leaseMu.Unlock()
		if lease == nil {
			refs := make([]roomActionImageRef, len(input.Images))
			for i, ref := range input.Images {
				refs[i] = roomActionImageRef{MessageSeq: ref.MessageSeq, AttachmentID: ref.AttachmentID}
			}
			var err error
			lease, err = svc.prepareRoomActionImages(ctx, refs, check)
			if err != nil {
				return gitlab.PreparedImageComment{}, err
			}
			selected = input
		} else if !reflect.DeepEqual(selected, input) {
			return gitlab.PreparedImageComment{}, ErrConflict
		}
		if err := lease.Check(ctx); err != nil {
			return gitlab.PreparedImageComment{}, err
		}
		message := gitlab.PreparedImageComment{Body: input.Body}
		for _, img := range lease.images {
			message.Images = append(message.Images, gitlab.ActionImage{Data: img.Data, MIME: img.Attachment.MIMEType, SHA256: img.SHA256})
		}
		return message, nil
	}
	checkImages := func(ctx context.Context) error {
		if err := check(ctx); err != nil {
			return err
		}
		leaseMu.Lock()
		defer leaseMu.Unlock()
		if lease != nil {
			return lease.Check(ctx)
		}
		return nil
	}
	upload := func(ctx context.Context, project string, index int, image gitlab.ActionImage, path string) (string, error) {
		ctx, finish := requestContext(ctx)
		defer finish()
		if err := checkImages(ctx); err != nil {
			return "", err
		}
		useCtx, credential, release, err := m.acquireGitLabAccess(ctx, id.RoomIdentity, admission.account, admission.secret, 30*time.Second)
		if err != nil {
			return "", err
		}
		defer release()
		if err := checkImages(useCtx); err != nil {
			return "", err
		}
		return gitlab.NewImageUploadAPIWithCredential(admission.account.URL, credential, m.platformHTTP)(useCtx, project, index, image, path)
	}
	write := func(ctx context.Context, q platform.TargetActionRequest, digest, body string) (gitlab.APIResponse, error) {
		ctx, finish := requestContext(ctx)
		defer finish()
		useCtx, credential, release, err := m.acquireGitLabAccess(ctx, id.RoomIdentity, admission.account, admission.secret, 30*time.Second)
		if err != nil {
			return gitlab.APIResponse{}, gitlab.ErrNoteNotSent
		}
		defer release()
		final, ok := ctx.Value(actionDispatchCheckKey{}).(actionDispatchCheck)
		if !ok || checkImages(useCtx) != nil || final(useCtx, q, digest) != nil {
			return gitlab.APIResponse{}, gitlab.ErrNoteNotSent
		}
		// q.Input stays frozen; only the native body argument contains Markdown.
		return gitlab.NewImageNoteAPIWithCredential(admission.account.URL, credential, m.platformHTTP)(useCtx, q.Target.Containers[0].ID.Value, gitlabActionIID(q), q.Target.Discussion.Value, body)
	}
	caps.Actions, err = gitlab.ImageReferenceActions(guardedRead, guardedWrite, write, upload, source, checkImages, admission.account.Username, executionInput)
	if err != nil {
		closeSession()
		return referenceActionSession{}, gitlabImageInputDiagnostic(err)
	}
	// Keep one absolute session deadline through Inspect, Execute and OAuth calls.
	for op, cap := range caps.Actions {
		inspect, execute := cap.Inspect, cap.Execute
		cap.Inspect = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
			ctx, finish := requestContext(ctx)
			defer finish()
			r, err := inspect(ctx, q, a)
			return r, gitlabImageInputDiagnostic(err)
		}
		cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
			ctx, finish := requestContext(ctx)
			defer finish()
			return execute(ctx, q, a)
		}
		caps.Actions[op] = cap
	}
	return referenceActionSession{Access: access, Capabilities: caps, Check: check, CommitCheck: commitCheck, Close: closeSession}, nil
}

// Selected connection evidence is shared by internal capabilities and Agent tools.
func (m *roomManager) actionAccountLocked(req toolsRequest) (registry.Account, error) {
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return registry.Account{}, err
	}
	file, err := m.platforms.Load()
	if err != nil {
		return registry.Account{}, err
	}
	b, d, err := m.resolveToolBindingLocked(f, req)
	if err != nil || d.Provider != "gitlab" {
		return registry.Account{}, ErrConflict
	}
	for _, c := range file.Platforms {
		if c.ID == b.PlatformID && c.Generation == b.PlatformGeneration && c.Provider == "gitlab" {
			a, _, err := registry.ResolveAccessAccount(file, c)
			return a, err
		}
	}
	return registry.Account{}, ErrConflict
}

func gitlabImageInputDiagnostic(err error) error {
	var input *gitlab.ImageInputError
	if errors.As(err, &input) {
		return invalidField(input.Field, input.Expect)
	}
	return err
}
func gitlabActionIID(q platform.TargetActionRequest) string {
	for _, alias := range q.Aliases {
		if alias.Object.ID.Namespace == "gitlab:rest:iid" {
			return alias.Object.ID.Value
		}
	}
	return ""
}
