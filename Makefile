TEST_PACKAGES ?= ./...
TEST_RUN ?= .
TEST_FLAGS ?=

.PHONY: test test-integration test-race test-e2e-check package-offline prepare-defaults

# Local feedback: use TEST_PACKAGES / TEST_RUN to select the affected scope.
# Stage regression: test-integration already includes core; do not run both in sequence.
test: TEST_TIMEOUT ?= 3m
test:
	go test $(TEST_FLAGS) -timeout=$(TEST_TIMEOUT) $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-integration: TEST_TIMEOUT ?= 8m
test-integration:
	go test $(TEST_FLAGS) -tags=integration -timeout=$(TEST_TIMEOUT) $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-race: TEST_TIMEOUT ?= 8m
test-race:
	go test $(TEST_FLAGS) -race -tags=integration -timeout=$(TEST_TIMEOUT) $(TEST_PACKAGES) -run '$(TEST_RUN)'

# Compilation only. Browser functions and long/capacity measurements are opt-in
# e2e scenarios with separate switches; see docs/testing.zh.md.
test-e2e-check:
	go test $(TEST_FLAGS) -tags=e2e $(TEST_PACKAGES) -run '^$$'

# Pass paths through the environment rather than interpolating them into shell code.
package-offline: export TEAMX_OFFLINE_ARCHIVE = $(ARCHIVE)
package-offline: export TEAMX_OFFLINE_OUTPUT = $(OUTPUT)
package-offline:
	node scripts/package-offline.mjs

# Stop the development service before refreshing its versioned initial templates.
prepare-defaults: export TEAMX_DEFAULTS_CONFIG = $(CONFIG)
prepare-defaults:
	node scripts/prepare-team-defaults.mjs "$${TEAMX_DEFAULTS_CONFIG:-.teamx/config.toml}"
