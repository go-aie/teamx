package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestActionDispatchFrozenContentAndExactRecord(t *testing.T) {
	for _, mode := range []string{"unchanged", "body", "target", "key", "operation", "digest", "account"} {
		t.Run(mode, func(t *testing.T) {
			f := referenceExecuteTest(t, "object")
			prepare := f.prepare
			digest := strings.Repeat("a", 64)
			f.prepare = func(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
				session, err := prepare(ctx, id, old)
				if err != nil {
					return session, err
				}
				cap := session.Capabilities.Actions[f.q.Operation]
				inspect, execute := cap.Inspect, cap.Execute
				cap.Inspect = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
					i, err := inspect(ctx, q, a)
					i.ContentDigest = digest
					return i, err
				}
				cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
					// An unrelated corrupt record would break any all-ledger scan. Dispatch
					// must read only this action and its exact context, without incrementing Attempts.
					path := filepath.Join(f.h.store.actionRoot(id), "records", referenceDigest("", "unrelated")+".json")
					if err := os.WriteFile(path, []byte(`{"version":999}`), 0600); err != nil {
						t.Fatal(err)
					}
					final := ctx.Value(actionDispatchCheckKey{}).(actionDispatchCheck)
					actual := q
					actualDigest := digest
					switch mode {
					case "body":
						actual.Input = json.RawMessage(`{"body":"replaced after ledger"}`)
					case "target":
						actual.Target.Object.ID.Value = "other"
					case "key":
						actual.IdempotencyKey = strings.Repeat("b", 32)
					case "operation":
						actual.Operation = platform.OperationMessageSend
					case "digest":
						actualDigest = strings.Repeat("b", 64)
					case "account":
						// The session-captured executing account is immutable as well.
						a.Account.ID.Value = "other"
					}
					var checkErr error
					if mode == "account" {
						checkErr = f.h.store.checkActionDispatch(id, f.q, actual, a, actualDigest)
					} else {
						checkErr = final(ctx, actual, actualDigest)
					}
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if mode == "unchanged" {
						if checkErr != nil {
							t.Fatal("exact dispatch scanned unrelated record", checkErr)
						}
						return execute(ctx, q, a)
					}
					if checkErr == nil {
						t.Fatal("dispatch accepted changed", mode)
					}
					return platform.TargetActionResult{Target: q.Target, Account: session.Access.Account, Status: "not_applied", RetryClass: "never"}, nil
				}
				session.Capabilities.Actions[f.q.Operation] = cap
				return session, nil
			}
			r, err := f.run()
			want := "not_applied"
			if mode == "unchanged" {
				want = "succeeded"
			}
			if err != nil || r.Status != want || r.Attempts != 1 {
				t.Fatal(r, err)
			}
		})
	}
}

func TestActionInputCanonicalNestedContent(t *testing.T) {
	var a, b ActionInput
	if err := json.Unmarshal([]byte(`{"body":"same","images":[{"message_seq":12,"attachment_id":"img_a"}]}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{ "images": [ { "attachment_id": "img_a", "message_seq": 12 } ], "body":"same" }`), &b); err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("JSON field order changed complete content identity")
	}
	digest := strings.Repeat("a", 64)
	if !equivalentActionContent(a, digest, b, digest) {
		t.Fatal("same prepared content not equivalent")
	}
	if equivalentActionContent(a, digest, b, strings.Repeat("b", 64)) {
		t.Fatal("Body-only unknown comparison")
	}
	b.Body = "same"
	b.raw = ""
	if equivalentActionContent(a, digest, b, "") {
		t.Fatal("structured unknown collapsed to text")
	}
	for _, d := range []string{"short", strings.Repeat("A", 64), strings.Repeat("x", 64)} {
		if validContentDigest(d) {
			t.Fatal("invalid digest", d)
		}
	}
}
