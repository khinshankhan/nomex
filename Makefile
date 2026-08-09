# Set .SHELLFLAGS to use "bash strict mode". This will fail on any error, fail
# on an error in a pipeline (usually it just returns the value of the last
# command in a pipeline), and will fail if using any undefined variables.
SHELL := bash
# Set .ONESHELL config. Runs the whole make recipe in one shell session.
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c
# Both of the above are GNU Make 3.82+. macOS ships 3.81 and ignores them,
# so a multi-command recipe there gets no strict mode and needs its own
# `set -e` to keep a failure from passing as success.
# Set .DELETE_ON_ERROR. Deletes any files generated on error.
.DELETE_ON_ERROR:
# Set makeflags to --warn-undefined-variables (probably an error) and to avoid
# the built-in rules (this removes a lot of magic that is related to yacc and
# other tools).
MAKEFLAGS += --warn-undefined-variables
MAKEFLAGS += --no-builtin-rules

# cgo-free on every platform, enforced by exporting this to every recipe: an
# accidental cgo dependency fails the build instead of sneaking in.
#
# The race detector needs cgo on some platforms, so `test` re-enables it for
# itself rather than losing the guarantee everywhere else.
export CGO_ENABLED := 0

# VARIABLES
#
# A service is any ./cmd/*/main.go. An empty cmd/ is fine -- `make build` on no
# services is a no-op, not an error -- so a binary starts building the day its
# main.go appears.
GOSERVICES = $(sort $(notdir $(realpath $(dir $(wildcard ./cmd/*/main.go)))))

.PHONY: $(GOSERVICES)

.PHONY: all
all: check build

# compile the library itself, plus any services
.PHONY: build
build: $(GOSERVICES)
	go build ./...

# creates a TARGET per go service
$(GOSERVICES): % : ./cmd/%/main.go
	go build -o bin/$@ ./cmd/$@

# list available go services
.PHONY: services
services:
	@echo $(GOSERVICES)

# extra flags and a package selector for the test targets, e.g.
#   make test GOTESTFLAGS=-v
#   make test GOTESTFLAGS='-run TestFoo -v'
#   make test PKG=./bootstrap/
GOTESTFLAGS ?=
PKG ?= ./...

# -race by default; it needs cgo, hence the local override of the repo-wide
# CGO_ENABLED=0.
.PHONY: test
test:
	CGO_ENABLED=1 go test -race $(GOTESTFLAGS) $(PKG)

# no race detector, for a quick inner loop or a cgo-less environment
.PHONY: test-quick
test-quick:
	go test $(GOTESTFLAGS) $(PKG)

.PHONY: cover
cover:
	CGO_ENABLED=1 go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# everything CI should gate on
.PHONY: check
check: fmt-check vet test

.PHONY: fmt
fmt:
	gofmt -w .
	go mod tidy

# fails rather than rewrites, so CI reports unformatted code instead of
# silently passing on a dirty tree
.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet:
	go vet ./...

.PHONY: clean
clean:
	rm -rf bin coverage.out

# platforms `cross` proves the build on. Each is a target of its own too, so
# `make darwin/arm64` checks just that one.
GOCROSSPLATFORMS = linux/amd64 windows/amd64 darwin/arm64

# prove everything builds cgo-free for every shipping platform. ./... rather
# than ./cmd/... so library packages are covered whether or not a binary exists.
.PHONY: cross $(GOCROSSPLATFORMS)
cross: $(GOCROSSPLATFORMS)

$(GOCROSSPLATFORMS):
	GOOS=$(@D) GOARCH=$(@F) go build ./...
