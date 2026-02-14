.PHONY: build test lint codegen clean

BINDIR ?= bin
GOFLAGS ?=

all: build

build: build-kedge build-hub build-agent

build-kedge:
	go build $(GOFLAGS) -o $(BINDIR)/kedge ./cmd/kedge/

build-hub:
	go build $(GOFLAGS) -o $(BINDIR)/kedge-hub ./cmd/kedge-hub/

build-agent:
	go build $(GOFLAGS) -o $(BINDIR)/kedge-agent ./cmd/kedge-agent/

test:
	go test ./...

test-util:
	go test ./pkg/util/...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

codegen:
	hack/update-codegen.sh

clean:
	rm -rf $(BINDIR)

verify: vet lint test
