.PHONY: build test check
build:
	go build -trimpath -o bin/statusbar-codex .
test:
	go test -race ./...
check:
	go vet ./...
