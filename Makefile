VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

export CGO_ENABLED = 0

.PHONY: build test check snapshot clean
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/codex-usage .
test:
	CGO_ENABLED=1 go test -race ./...
check:
	go vet ./...
	test -z "$$(gofmt -l .)"
snapshot:
	goreleaser release --snapshot --clean
clean:
	rm -rf bin dist
