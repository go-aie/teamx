package lark

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"

	"github.com/go-aie/teamx/internal/app/platformtools"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// ActionHost captures one fixed account and Call. Operations touch the original
// owners under their existing locks; this adapter never sees a manager or Store.
type ActionHost struct {
	Context           context.Context
	Access            platform.AccessContext
	AccountLabel      string
	Pin               *lark.Pinned
	Check             func(context.Context) error
	CheckSelf         func(context.Context, platform.TargetIdentity) error
	Images            func(context.Context, []platformtools.ImageRef, func(context.Context) error) (platformtools.ImageLease, error)
	ValidateInput     func(json.RawMessage, bool) error
	Request, Conflict error
	Close             func()
}

func BindActions(host ActionHost, capabilities func(lark.SendReader, lark.ReplyWriter, lark.SendWriter) platform.ReferenceCapabilities, images bool) (platformtools.ActionSession, error) {
	callCtx, access, pin, check := host.Context, host.Access, host.Pin, host.Check
	// Capabilities always execute on the admitted context: binding revocation,
	// member removal and shutdown synchronously cancel blocking CLI children.
	merge := func(ctx context.Context) (context.Context, func()) {
		var merged context.Context
		var cancel context.CancelFunc
		if deadline, ok := ctx.Deadline(); ok {
			merged, cancel = context.WithDeadline(callCtx, deadline)
		} else {
			merged, cancel = context.WithCancel(callCtx)
		}
		if final, ok := ctx.Value(platformtools.DispatchCheckKey{}).(platformtools.DispatchCheck); ok {
			merged = context.WithValue(merged, platformtools.DispatchCheckKey{}, final)
		}
		stop := context.AfterFunc(ctx, cancel)
		if ctx.Err() != nil {
			cancel()
		}
		return merged, func() { stop(); cancel() }
	}
	caps := capabilities(pin.SendReader(), func(ctx context.Context, message, body, key string) (string, error) {
		ctx, finish := merge(ctx)
		defer finish()
		return pin.ReplyText(ctx, message, body, key, check)
	}, func(ctx context.Context, target platform.TargetIdentity, body, key string) (string, error) {
		ctx, finish := merge(ctx)
		defer finish()
		guard := check
		if target.Object.Type == "user" && target.Object.ID == access.Account.ID {
			guard = func(ctx context.Context) error {
				if err := check(ctx); err != nil {
					return err
				}
				return host.CheckSelf(ctx, target)
			}
		}
		return pin.SendText(ctx, target, body, key, guard)
	})

	var lease platformtools.ImageLease
	var leaseMu sync.Mutex
	var selected lark.ImageSendInput
	if images {
		source := func(ctx context.Context, input lark.ImageSendInput) (lark.PreparedImageMessage, error) {
			leaseMu.Lock()
			defer leaseMu.Unlock()
			if lease == nil {
				refs := make([]platformtools.ImageRef, len(input.Images))
				for i, ref := range input.Images {
					refs[i] = platformtools.ImageRef{MessageSeq: ref.MessageSeq, AttachmentID: ref.AttachmentID}
				}
				var err error
				lease, err = host.Images(ctx, refs, check)
				if err != nil {
					return lark.PreparedImageMessage{}, err
				}
				selected = input
			} else if !reflect.DeepEqual(selected, input) {
				return lark.PreparedImageMessage{}, host.Conflict
			}
			if err := lease.Check(ctx); err != nil {
				return lark.PreparedImageMessage{}, err
			}
			message := lark.PreparedImageMessage{Body: input.Body}
			for _, img := range lease.Bytes() {
				message.Images = append(message.Images, lark.SendImage{Data: img.Data, MIME: img.MIME, SHA256: img.SHA256})
			}
			return message, nil
		}
		caps = lark.WithImageActions(caps, pin.SendReader(), source, func(ctx context.Context, q platform.TargetActionRequest, message lark.PreparedImageMessage) (string, []string, error) {
			ctx, finish := merge(ctx)
			defer finish()
			final, ok := ctx.Value(platformtools.DispatchCheckKey{}).(platformtools.DispatchCheck)
			if !ok || lease == nil {
				return "", nil, lark.ErrSendNotSent
			}
			return pin.SendImages(ctx, q.Target, message, q.IdempotencyKey, lease.Check, func(ctx context.Context, digest string) error { return final(ctx, q, digest) })
		}, pin.ImageUploadReady)
	}
	resolve := caps.Resolve
	caps.Resolve = func(ctx context.Context, l platform.TargetLocator, a platform.AccessContext) (platform.TargetResolution, error) {
		if err := check(ctx); err != nil {
			return platform.TargetResolution{}, err
		}
		ctx, finish := merge(ctx)
		defer finish()
		r, err := resolve(ctx, l, a)
		r.AccountLabel = host.AccountLabel
		if e := check(ctx); e != nil {
			return platform.TargetResolution{}, e
		}
		return r, err
	}
	// Keep inspection and post-write verification within the same managed call.
	for op, cap := range caps.Actions {
		inspect, execute := cap.Inspect, cap.Execute
		cap.Inspect = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
			if err := check(ctx); err != nil {
				return platform.TargetActionInspection{}, err
			}
			if q.Operation == platform.OperationMessageSend || q.Operation == platform.OperationMessageSendSelf {
				if err := host.ValidateInput(q.Input, images); err != nil {
					return platform.TargetActionInspection{}, err
				}
			}
			ctx, finish := merge(ctx)
			defer finish()
			inspection, err := inspect(ctx, q, a)
			var native *lark.Error
			if images && errors.As(err, &native) {
				if native.Code == "invalid_send_input" {
					return inspection, &platformtools.Diagnostic{Cause: host.Request, Field: "input", Message: "provide a nonempty body and/or 1-4 distinct {message_seq,attachment_id} Room images; paths, URLs and resource keys are not accepted"}
				}
				if strings.HasPrefix(native.Code, "image_upload_") {
					return inspection, &platformtools.Diagnostic{Cause: host.Conflict, Conflict: true, Field: "input.images", Message: native.Code, Hint: larkImagePermissionHint(native.Code)}
				}
			}
			return inspection, err
		}
		cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
			ctx, finish := merge(ctx)
			defer finish()
			// Preserve a known outcome on settlement; do not post-check a revoked binding.
			return execute(ctx, q, a)
		}
		caps.Actions[op] = cap
	}
	return platformtools.ActionSession{Access: access, Capabilities: caps, Check: check, Close: func() {
		leaseMu.Lock()
		if lease != nil {
			lease.Close()
		}
		leaseMu.Unlock()
		pin.Close()
		host.Close()
	}}, nil
}

func larkImagePermissionHint(code string) string {
	hint := "retry_check: refresh image.upload on the original connection; unchecked or expired observations cannot authorize an upload."
	switch code {
	case "image_upload_application_scopes_missing":
		hint = "configure_application: open this connection's official application permissions page and enable user im:resource; user login alone cannot repair an application gap."
	case "image_upload_user_scopes_missing", "image_upload_credential_unavailable":
		hint = "authorize_user: when the user requests repair, read platforms skill and use connect authorize on this exact existing account; preserve member grants and notification settings."
	case "image_upload_account_pinning_unavailable", "image_upload_identity_mismatch":
		hint = "revalidate_account: verify the original fixed user account in settings; do not switch identities. An unsupported host cannot be repaired by login."
	}
	return hint + " Text sending remains independent. No message was sent; do not fall back to text or a subset of images."
}
