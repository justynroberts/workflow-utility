BINARY  := pdwf
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help build test lint install release clean

help: ## Show targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

build: ## Build ./bin/pdwf
	go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) .

test: ## Run all tests (uses a fake PagerDuty, no network)
	go test ./...

lint: ## gofmt check + go vet
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run: gofmt -w ." && exit 1)
	go vet ./...

PREFIX ?= $(HOME)/.local/bin

install: build ## Copy pdwf into ~/.local/bin (override with PREFIX=...)
	mkdir -p $(PREFIX)
	cp bin/$(BINARY) $(PREFIX)/$(BINARY)
	chmod 0755 $(PREFIX)/$(BINARY)
	@echo "installed $(PREFIX)/$(BINARY) - open a new terminal (or run: rehash) and type: pdwf help"

release: ## Cross-compile for macOS, Linux and Windows into ./dist
	@mkdir -p dist
	GOOS=darwin  GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-arm64 .
	GOOS=darwin  GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-amd64 .
	GOOS=linux   GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-amd64 .
	GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-windows-amd64.exe .

clean: ## Remove build output
	rm -rf bin dist
