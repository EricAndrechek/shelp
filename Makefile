PREFIX ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test vet test-shell bench-build bench
build:
	go build -ldflags '$(LDFLAGS)' -o bin/shelp ./cmd/shelp

install:
	go build -ldflags '$(LDFLAGS)' -o $(PREFIX)/bin/shelp ./cmd/shelp

test:
	go test ./...

vet:
	gofmt -l . | (! grep .) && go vet ./...

# Real interactive pty tests for the shell integration (raw line mode,
# --bind) across zsh/bash (run directly) and fish (a throwaway Docker
# container, since fish isn't installed on this Mac). See
# scripts/test-shell-integration.py's docstring and CLAUDE.md's shell-v2
# plan for the test matrix. Needs python3 and, for fish, docker.
test-shell:
	python3 scripts/test-shell-integration.py

bench-build:
	go build -ldflags '$(LDFLAGS)' -o bin/shelp-bench ./cmd/shelp-bench

# Run the benchmark against every available provider on this machine (see
# bench/README.md for flags: --providers, --templates, --cases, --samples).
bench: bench-build
	./bin/shelp-bench run
