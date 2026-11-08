TEST_PACKAGES ?= ./...
TEST_RUN ?= .

.PHONY: test test-integration test-race test-e2e-check

test:
	go test -timeout=3m $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-integration:
	go test -tags=integration -timeout=8m $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-race:
	go test -race -tags=integration -timeout=8m $(TEST_PACKAGES) -run '$(TEST_RUN)'

test-e2e-check:
	go test -tags=e2e $(TEST_PACKAGES) -run '^$$'
