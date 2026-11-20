TEST_PACKAGES ?= ./...
TEST_RUN ?= .

.PHONY: test test-integration test-race test-e2e-check package-offline

# Local feedback: use TEST_PACKAGES / TEST_RUN to select the affected scope.
# Stage regression: test-integration already includes core; do not run both in sequence.
test: TEST_TIMEOUT ?= 3m
test:
	go test -timeout=$(TEST_TIMEOUT) $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-integration: TEST_TIMEOUT ?= 8m
test-integration:
	go test -tags=integration -timeout=$(TEST_TIMEOUT) $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-race: TEST_TIMEOUT ?= 8m
test-race:
	go test -race -tags=integration -timeout=$(TEST_TIMEOUT) $(TEST_PACKAGES) -run '$(TEST_RUN)'

# Compilation only. Browser functions and long/capacity measurements are opt-in
# e2e scenarios with separate switches; see docs/testing.zh.md.
test-e2e-check:
	go test -tags=e2e $(TEST_PACKAGES) -run '^$$'

# Pass paths through the environment rather than interpolating them into shell code.
package-offline: export TEAMX_OFFLINE_ARCHIVE = $(ARCHIVE)
package-offline: export TEAMX_OFFLINE_OUTPUT = $(OUTPUT)
package-offline:
	node scripts/package-offline.mjs
