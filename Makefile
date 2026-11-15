BIN := bin/boxt
PREFIX ?= /usr/local
BINDIR := $(PREFIX)/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/daveagill/boxt/internal/cli.Version=$(VERSION)

.PHONY: build install test integration vet fmt check clean snapshot release

build:
	@mkdir -p $(dir $(BIN))
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/boxt

install: build
	install -d $(BINDIR)
	install -m 0755 $(BIN) $(BINDIR)/boxt

test:
	go test ./...

integration:
	make build
	BOXT_INTEGRATION=1 BOXT_BIN=$(abspath $(BIN)) go test -tags=integration ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

check: fmt vet test

clean:
	rm -rf bin dist

snapshot:
	goreleaser release --snapshot --clean

release:
	goreleaser release --clean
