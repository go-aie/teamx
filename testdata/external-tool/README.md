# External local tool fixture

This is a separate Go module. Its Go source imports only TeamX's public
`platform`, `extension`, and `boot` packages; it declares no Provider or account.

From the TeamX checkout:

```sh
python3 testdata/external-tool/acceptance.py \
  --teamx-root "$PWD" \
  --output "$PWD/.local/external-tool/new-run"
```

Use a fresh output directory. The driver copies this module there, sets
`GOWORK=off`, and adds an explicit temporary `replace` for the supplied checkout.
The checked-in `v0.0.0` requirement is a development fixture placeholder, not a
release recommendation. A distribution must pin a real published version that
contains these APIs; this task does not publish one.

The driver builds the custom TeamX entry and its sibling echo executable, checks
the explicit selection, then starts an isolated loopback service. It creates a
paused member and binds the accountless tool through the existing HTTP API. An
offline ACP subprocess uses the ordinary temporary execution credential to run
the actual CLI discovery and execution paths. It verifies argument/stdin bytes,
credential exclusion from the tool process, exit status 23, rejection of admin
impersonation, cancellation, and clean service exit. It creates no platform
connection or remote Workspace binding and makes no remote business writes.

`acp_fixture.py` is a deterministic transport fixture, not DSH or a model. It
keeps the prompt open until cancellation and manufactures no formal input
receipt. Results and the service log are saved under the output directory;
real backend execution and recovery require their own acceptance.

The same check runs with:

```sh
go test -tags=integration ./boot \
  -run '^TestExternalModuleLocalToolThroughPublicStartup$' -count=1 -timeout=2m
```
