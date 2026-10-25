GO ?= go
GOVULNCHECK_VERSION ?= v1.9.0

.PHONY: check fmt fmt-check mod-check vet test race vulncheck build cross-build

check: fmt-check mod-check vet test vulncheck

fmt:
	$(GO) fmt ./...

fmt-check:
	@files=$$(gofmt -l cmd internal); if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

mod-check:
	$(GO) mod tidy -diff
	$(GO) mod verify

vet:
	CGO_ENABLED=0 $(GO) vet ./...

test:
	CGO_ENABLED=0 $(GO) test -count=1 -timeout=5m ./...

# The race detector requires CGO, although normal builds do not.
race:
	CGO_ENABLED=1 $(GO) test -race -count=1 -timeout=5m ./...

vulncheck:
	@set -eu; tool_dir=$$(mktemp -d); trap 'rm -rf "$$tool_dir"' EXIT; \
		GOBIN="$$tool_dir" GOOS= GOARCH= CGO_ENABLED=0 $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION); \
		CGO_ENABLED=0 "$$tool_dir/govulncheck" ./...

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o laterr ./cmd/laterr

cross-build:
	@set -eu; for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do \
		echo "building $$target"; \
		CGO_ENABLED=0 GOOS=$${target%/*} GOARCH=$${target#*/} $(GO) build -trimpath -o /dev/null ./cmd/laterr; \
	done
