GO ?= go
export CGO_ENABLED=0

.PHONY: all build test vet lint check cross clean

all: check build

build:
	$(GO) build -trimpath -o golab ./cmd/golab

test:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

check: vet test

cross:
	$(GO) tool dist list | grep -E '^(linux|darwin|windows|freebsd)/' | while read t; do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -o /tmp/opencode/golab-$$os-$$arch ./cmd/golab || exit 1; \
	done

clean:
	rm -f golab
