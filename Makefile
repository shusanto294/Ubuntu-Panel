# Ubuntu Panel — build
#
# The frontend is built first and embedded into the binary, so `make` produces
# one file that needs nothing beside it on the server.

BINARY  := ubuntu-panel
PREFIX  ?= /opt/ubuntu-panel
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Trim the build paths and symbol table: nothing debugs this binary on the
# server, and the paths would name this machine's home directory.
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: all frontend build clean install test vet fmt release

all: frontend build

frontend: node_modules
	npm run build

node_modules: package-lock.json
	npm ci
	@touch node_modules

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/ubuntu-panel

# The panel targets Linux servers; this is what a release is built with. No
# cgo means no cross-compiler is needed for either architecture.
release: frontend
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 ./cmd/ubuntu-panel
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 ./cmd/ubuntu-panel

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

install: all
	install -d $(PREFIX)/bin
	install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY)

clean:
	rm -rf $(BINARY) dist internal/assets/public/build
