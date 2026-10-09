PREFIX ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test vet test-shell bench-build bench sign-shortcut ci
build:
	go build -ldflags '$(LDFLAGS)' -o bin/shelp ./cmd/shelp

install:
	go build -ldflags '$(LDFLAGS)' -o $(PREFIX)/bin/shelp ./cmd/shelp

test:
	go test ./...

# Exactly what CI (.github/workflows/ci.yml) runs, and what to run before
# tagging: vet, the race-enabled Go tests, a build, and the shell suite.
# Never signs anything (sign-shortcut is a manual, iCloud-Mac-only step);
# tests that need a real Mac skip themselves when CI is set.
ci: vet build test-shell
	go test -race ./...

vet:
	gofmt -l . | (! grep .) && go vet ./...

# Sign the current shortcut format with Apple and store it (plus its
# expiry and generator fingerprint) in internal/provider/signed/, which
# shelp embeds so `shelp setup shortcuts` needs no trip to Apple. Run on
# macOS whenever the generator changes; see internal/provider/signed.go.
sign-shortcut:
	go run ./internal/tools/signshortcut

# Real interactive pty tests for the shell integration (raw line mode,
# --bind, the thinking status, Ctrl-C) in zsh, bash and fish. fish runs
# natively when a `fish` is on PATH, else in a throwaway `docker run --rm`
# container; with neither, the fish checks fail rather than skip
# (SHELP_TEST_FISH=docker|native|<path> overrides the choice). Needs
# python3, Go, zsh, bash >= 4 (macOS: Homebrew's; /bin/bash is 3.2), uv or
# pyte (the suite re-runs itself under `uv run --with pyte`; without either
# the screen checks are weaker byte checks), and fish or Docker. See
# scripts/test-shell-integration.py's docstring and CLAUDE.md's
# internal/shellinit notes.
test-shell:
	python3 scripts/test-shell-integration.py

bench-build:
	go build -ldflags '$(LDFLAGS)' -o bin/shelp-bench ./cmd/shelp-bench

# Run the benchmark against every available provider on this machine (see
# bench/README.md for flags: --providers, --templates, --cases, --samples).
bench: bench-build
	./bin/shelp-bench run
