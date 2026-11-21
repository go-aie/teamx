# External notification module

This separate Go module uses only public TeamX packages. Its accountless source emits one deterministic notification through the normal source runner, durable pending state and Inbox receipt. It supplies the required local input codec; no model, CLI provider account, Workspace or remote business operation is used.

Run from the TeamX root:

```sh
python3 testdata/external-notification/acceptance.py \
  --teamx-root "$PWD" \
  --output "$PWD/.local/external-notification-new"
```

Use a new output directory. The script copies the module, sets `GOWORK=off` and adds a temporary local replace to this checkout. It leaves the root go.mod/go.sum unchanged. The fixture's `v0.0.0` is a local placeholder; a released extension must pin an actual published TeamX version.

The script creates an isolated service, Room and member. While the service is stopped, it provisions the source-owned, explicit opt-in snapshot with the exact Room/member identity and a 32-character lowercase hexadecimal subscription ID. This fixture provisioning does not introduce a general production subscription management API.

Normal startup loads the selected source and invokes its recovery check. The runner commits pending content; the existing Inbox owner durably saves it to JSONL before the source acknowledgement clears pending. The script checks human preview without consumption, process restart without duplication, and rejection with `input_version_unsupported` when a second fixture selection omits the saved partition's codec. Saved JSONL hashes remain unchanged after that rejection. Four service shutdowns must exit successfully.

`result.json` records these checks. The leading `--fixture-without-codec` flag belongs only to this test executable. For the automated wrapper, run `go test -tags=integration ./boot -run '^TestExternalModuleNotificationThroughPublicStartup$' -count=1`.
