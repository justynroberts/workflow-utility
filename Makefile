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

release: ## Cross-compile for macOS, Linux and Windows into ./dist, with checksums.txt
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-arm64 .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-amd64 .
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-arm64 .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-windows-amd64.exe .
	cd dist && shasum -a 256 $(BINARY)-* > checksums.txt
	@cat dist/checksums.txt

clean: ## Remove build output
	rm -rf bin dist
