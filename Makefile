GO ?= go
BIN := bin
# A -race cgo-t igényel; C fordító nélkül: make test RACE=
RACE ?= -race

.PHONY: build build-all test lint proto proto-check clean

build:
	$(GO) build -o $(BIN)/ ./cmd/...

# A hoston és a sandboxban futó binárisok (PRD: macOS + Linux, arm64 + amd64).
build-all:
	@for os in darwin linux; do for arch in arm64 amd64; do \
		echo "$$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -o $(BIN)/$$os-$$arch/ ./cmd/... || exit 1; \
	done; done

test:
	$(GO) test $(RACE) ./...

lint:
	golangci-lint run ./...
	buf lint

proto:
	buf generate

# CI: a generált kód naprakész-e.
proto-check: proto
	git diff --exit-code -- gen/

clean:
	rm -rf $(BIN) dist
