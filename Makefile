PREFIX ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test vet bench-build
build:
	go build -ldflags '$(LDFLAGS)' -o bin/shelp ./cmd/shelp

install:
	go build -ldflags '$(LDFLAGS)' -o $(PREFIX)/bin/shelp ./cmd/shelp

test:
	go test ./...

vet:
	gofmt -l . | (! grep .) && go vet ./...
