# Frozen four-tool discovery baseline

This package preserves the stage 4 catalog, scope/filter, stats and preview behavior for historical regression and paired model comparisons. Application code does not import it. It shares immutable input types and platform codecs with the storage kernel, and builds an in-memory snapshot; it does not maintain another Store or consumption ledger.

The current application contract is `inbox_streams/v3`, implemented in `internal/inputstream` and `internal/room`. Keep prior model evidence unchanged when revising that contract.
