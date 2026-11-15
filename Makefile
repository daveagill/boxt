BIN := bin/boxt
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/boxt/boxt/internal/cli.Version=$(VERSION)

.PHONY: build test integration vet fmt check clean

build:
	@mkdir -p $(dir $(BIN))
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/boxt

test:
	go test ./...

integration:
	BOXT_INTEGRATION=1 go test -tags=integration ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

check: fmt vet test

clean:
	rm -rf bin
