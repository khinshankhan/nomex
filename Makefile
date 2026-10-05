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

# cgo free on every platform, so an accidental cgo dependency fails the build.
# `test` turns it back on for itself since the race detector needs it.
export CGO_ENABLED := 0

# goose reads its config from the environment. These aren't secrets, so they
# live here and a fresh clone can `make migrate` right away.
#
# NOTE: the driver is "sqlite" (modernc, pure Go), not "sqlite3" which needs cgo.
# The database lives under var/ (gitignored). For a scratch copy:
# `make migrate DB=./var/scratch.db`
# Defined before GOOSE_DBSTRING because := expands immediately.
DB ?= ./var/nomex.db

export GOOSE_DRIVER := sqlite
export GOOSE_MIGRATION_DIR := ./migrations
export GOOSE_DBSTRING := $(DB)

# VARIABLES
#
# A service is any ./cmd/*/main.go. With none, `make build` is a no-op.
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

# MIGRATIONS
#
# var/ is created on demand since goose won't create it
$(dir $(DB)):
	mkdir -p $@

# goose prefixes every line with a timestamp, which breaks its own alignment
GOOSE_STRIP_TS = sed -E 's/^[0-9]{4}\/[0-9]{2}\/[0-9]{2} [0-9:]{8}[[:space:]]*//'

.PHONY: migrate
migrate: | $(dir $(DB))
	@goose up 2>&1 | $(GOOSE_STRIP_TS)

# goose's status table doesn't align, so this joins migrations/ against
# goose_db_version instead and shows unapplied files as pending
.PHONY: migrate-status
migrate-status: | $(dir $(DB))
	@applied=$$(test -f $(DB) && sqlite3 -batch -noheader $(DB) \
		".timer off" ".changes off" \
		"SELECT version_id || ' ' || datetime(tstamp,'localtime') \
		 FROM goose_db_version WHERE version_id > 0;" 2>/dev/null || true); \
	ls $(GOOSE_MIGRATION_DIR)/*.sql 2>/dev/null | awk -v applied="$$applied" '\
		BEGIN { \
			n = split(applied, rows, "\n"); \
			for (i = 1; i <= n; i++) if (rows[i] != "") { \
				split(rows[i], f, " "); at[f[1]] = f[2] " " f[3]; \
			} \
			printf "%-16s %-8s %-20s %s\n", "VERSION", "STATUS", "APPLIED AT", "NAME"; \
		} \
		{ \
			file = $$0; sub(/.*\//, "", file); \
			split(file, p, "_"); v = p[1]; \
			name = file; sub(/^[0-9]+_/, "", name); sub(/\.sql$$/, "", name); \
			printf "%-16s %-8s %-20s %s\n", v, \
				(v in at ? "applied" : "pending"), \
				(v in at ? at[v] : "--"), name; \
		}'

# one step back, for iterating on the newest migration
.PHONY: migrate-down
migrate-down:
	@goose down 2>&1 | $(GOOSE_STRIP_TS)

# `make migration name=add_foo` -- goose timestamps it
.PHONY: migration
migration:
	@test -n "$(name)" || { echo "usage: make migration name=<snake_case>"; exit 1; }
	@goose create $(name) sql 2>&1 | $(GOOSE_STRIP_TS)

# rebuild the database from scratch. Unpushed migrations get edited in place
# and goose won't re-run an applied version, so this is how those land locally.
.PHONY: db-reset
db-reset:
	rm -f $(DB) $(DB)-wal $(DB)-shm
	$(MAKE) migrate

# dump the live schema as SQL, which can differ from migrations/ after an
# in-place edit
.PHONY: schema
schema:
	@test -f $(DB) || { echo "$(DB) does not exist; run make migrate"; exit 1; }
	@sqlite3 $(DB) .schema

# regenerate the typed query layer from migrations/ + queries/
.PHONY: sqlc
sqlc:
	sqlc generate

# fails instead of rewriting, so CI catches stale generated code
.PHONY: sqlc-check
sqlc-check:
	sqlc diff

# extra flags and a package selector for the test targets, eg
#   make test GOTESTFLAGS=-v
#   make test GOTESTFLAGS='-run TestFoo -v'
#   make test PKG=./bootstrap/
GOTESTFLAGS ?=
PKG ?= ./...

# -race by default, which needs cgo, hence the local CGO_ENABLED override
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

# everything CI should gate on, including sqlc-check so drifted generated code
# fails here instead of as a confusing type error later
.PHONY: check
check: fmt-check vet sqlc-check test

.PHONY: fmt
fmt:
	gofmt -w .
	go mod tidy

# fails instead of rewriting, so CI catches unformatted code
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

# check everything builds cgo free on every shipping platform, ./... so library
# packages are covered too
.PHONY: cross $(GOCROSSPLATFORMS)
cross: $(GOCROSSPLATFORMS)

$(GOCROSSPLATFORMS):
	GOOS=$(@D) GOARCH=$(@F) go build ./...
