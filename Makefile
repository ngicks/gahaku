GO ?= go
BUF ?= buf
GOLANGCI_LINT ?= golangci-lint

.DEFAULT_GOAL := build

.PHONY: generate
generate:
	cd api && $(BUF) generate

.PHONY: build
build:
	$(GO) build ./...

.PHONY: test
test:
	$(GO) test ./...

# The end-to-end suite is opt-in — it builds the binary and rasterizes real
# documents — so `test` above skips it. Its presigned round trip starts
# versitygw with podman; where podman cannot run containers, point
# GAHAKU_TEST_VERSITYGW_ENDPOINT at a gateway already listening with the root
# credentials e2e/versitygw_test.go presigns with.
.PHONY: e2e
e2e:
	GAHAKU_TEST_E2E=1 $(GO) test ./e2e/ -count=1 -v -timeout 30m

# The deployment harness is opt-in for the same reason: it pulls hundreds of
# megabytes of images and needs a podman that can run a privileged container.
# It skips rather than fails where any of that is missing. The timeout clears a
# cold pull of the systemd image plus the inner pull of the gateway's, since a
# `panic: test timed out` would lose the diagnostics the harness prints on a
# real convergence failure.
.PHONY: test-quadlet
test-quadlet:
	GAHAKU_TEST_QUADLET=1 $(GO) test ./deployment/test/ -count=1 -v -timeout 45m

.PHONY: lint
lint:
	cd api && $(BUF) lint
	$(GO) vet ./...
	$(GOLANGCI_LINT) run

.PHONY: fmt
fmt:
	cd api && $(BUF) format --write
	$(GOLANGCI_LINT) fmt

.PHONY: tidy
tidy:
	$(GO) mod tidy

# Needs a committed baseline: fails until api/ exists on the main branch.
.PHONY: proto-breaking
proto-breaking:
	cd api && $(BUF) breaking --against '../.git#branch=main,subdir=api'
