BIN        := aurora
PKG        := ./cmd/aurora
DIST       := dist
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE       ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w \
	-X github.com/aurora/aurora/internal/buildinfo.Version=$(VERSION) \
	-X github.com/aurora/aurora/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/aurora/aurora/internal/buildinfo.Date=$(DATE)

# Aurora is a server: never link the race detector or coverage in.
GOFLAGS_BASE := -trimpath

.PHONY: all build install clean test vet fmt lint examples cross release help

all: build

## build: compile the core binary for the host platform
build:
	CGO_ENABLED=0 go build $(GOFLAGS_BASE) -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)
	@echo "→ ./$(BIN)  ($$(du -h $(BIN) | cut -f1))"

## install: build and copy to ~/bin
install: build
	@mkdir -p $$HOME/bin
	@install -m 755 $(BIN) $$HOME/bin/$(BIN)
	@echo "→ $$HOME/bin/$(BIN)"

## test: run the unit and integration tests
test:
	go test ./... -count=1

## vet: run go vet
vet:
	go vet ./...

## fmt: format every Go file
fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './dist/*')

## lint: fmt + vet
lint: fmt vet

## examples: build the Go example plugin
examples:
	cd plugins/hello && CGO_ENABLED=0 go build -trimpath -o hello .

## cross: build release binaries for Termux phones
cross:
	@mkdir -p $(DIST)
	@for pair in android/arm64 android/arm linux/amd64 linux/arm64; do \
		os=$${pair%/*}; arch=$${pair#*/}; \
		out="$(DIST)/aurora-$$os-$$arch"; \
		echo "→ $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build $(GOFLAGS_BASE) -ldflags "$(LDFLAGS)" -o $$out $(PKG); \
	done
	@ls -lh $(DIST)

## release: cross-build and archive
release: cross
	@cd $(DIST) && for f in aurora-*; do \
		case $$f in *.tar.gz) ;; *) tar -czf $$f.tar.gz $$f && rm -f $$f ;; esac; \
	done
	@ls -lh $(DIST)

## clean: remove build artifacts
clean:
	rm -rf $(BIN) $(DIST) plugins/hello/hello

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
