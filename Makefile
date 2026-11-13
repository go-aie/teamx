TEST_PACKAGES ?= ./...
TEST_RUN ?= .

.PHONY: test test-integration test-race test-e2e-check package-offline

test:
	go test -timeout=3m $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-integration:
	go test -tags=integration -timeout=8m $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-race:
	go test -race -tags=integration -timeout=8m $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-e2e-check:
	go test -tags=e2e $(TEST_PACKAGES) -run '^$$'

# Pass paths through the environment rather than interpolating them into shell code.
package-offline: export TEAMX_OFFLINE_ARCHIVE = $(ARCHIVE)
package-offline: export TEAMX_OFFLINE_OUTPUT = $(OUTPUT)
package-offline:
	node scripts/package-offline.mjs
