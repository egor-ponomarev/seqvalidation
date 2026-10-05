.DEFAULT_GOAL := check

GO_MIN := 1.22.12
GO_CURRENT := 1.27.1
GOLANGCI_VERSION := 2.14.0
GOVULNCHECK_VERSION := v1.8.0
TOOLS_DIR ?= $(CURDIR)/.tools
LINTER := $(TOOLS_DIR)/golangci-lint-$(GOLANGCI_VERSION)

export GOTOOLCHAIN := go$(GO_CURRENT)

.PHONY: check fmt-check test test-min test-race vet linters-install lint vuln

check: fmt-check test-min test-race vet lint vuln

fmt-check:
	@set -e; \
	seq_goroot="$$(go env GOROOT)"; \
	unformatted="$$("$$seq_goroot/bin/gofmt" -l .)"; \
	if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi

test:
	go test -count=1 ./...

test-min:
	GOTOOLCHAIN=go$(GO_MIN) go test -count=1 ./...

test-race:
	go test -race -count=1 ./...

vet:
	go vet ./...

linters-install:
	@if [ ! -x "$(LINTER)" ] || [ "$$("$(LINTER)" version --short)" != "$(GOLANGCI_VERSION)" ]; then \
		sh scripts/install_linter.sh "$(GOLANGCI_VERSION)" "$(LINTER)"; \
	fi

lint: linters-install
	"$(LINTER)" config verify
	"$(LINTER)" run ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
