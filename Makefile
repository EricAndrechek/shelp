PREFIX ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test vet bench-build bench
build:
	go build -ldflags '$(LDFLAGS)' -o bin/shelp ./cmd/shelp

install:
	go build -ldflags '$(LDFLAGS)' -o $(PREFIX)/bin/shelp ./cmd/shelp

test:
	go test ./...

vet:
	gofmt -l . | (! grep .) && go vet ./...

bench-build:
	go build -ldflags '$(LDFLAGS)' -o bin/shelp-bench ./cmd/shelp-bench

# Run the benchmark against every available provider on this machine (see
# bench/README.md for flags: --providers, --templates, --cases, --samples).
bench: bench-build
	./bin/shelp-bench run
