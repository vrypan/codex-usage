.PHONY: build test check
build:
	go build -trimpath -o bin/codex-usage .
test:
	go test -race ./...
check:
	go vet ./...
