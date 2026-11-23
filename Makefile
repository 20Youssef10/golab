GO ?= go
GOLANGCI_LINT ?= golangci-lint
export CGO_ENABLED=0

.PHONY: all build test vet lint check cross cross-smoke coverage coverage-gate goleak-check fuzz redaction paths clean

all: check build

build:
	$(GO) build -trimpath -o golab ./cmd/golab

test:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

# Mandatory, fast gates on every PR. Shell safety scripts and the linter are
# wired here and in .github/workflows/ci.yml (AUDIT-03 §4.2).
redaction:
	bash scripts/check-redaction.sh

paths:
	bash scripts/check-paths.sh

check: vet test lint redaction paths

# Full cross matrix. Output lives in ./dist so it works on a clean CI runner
# (P0-4); cross-smoke validates the artifacts and executes the host binary.
cross:
	mkdir -p dist
	$(GO) tool dist list | grep -E '^(linux|darwin|windows|freebsd)/' | while read t; do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -o dist/golab-$$os-$$arch ./cmd/golab || exit 1; \
	done

cross-smoke: cross
	bash scripts/cross-smoke.sh dist

# Coverage: docs/07 §11 targets 80% for colabapi/runtime/state/sshx/auth.
# coverage-gate reads dist/coverage.out; COVERAGE_MIN overrides the threshold.
coverage:
	mkdir -p dist
	$(GO) test -covermode=atomic -coverprofile=dist/coverage.out ./...

coverage-gate: coverage
	bash scripts/coverage-gate.sh dist/coverage.out

# goleak wiring gate (ADVISORY=1 makes it non-blocking while TestMains land).
goleak-check:
	bash scripts/goleak-check.sh

# Short fuzz smoke for both seed corpora; nightly workflow does the long run.
fuzz:
	$(GO) test ./internal/runtime -run='^$$' -fuzz=FuzzDecodeFrame -fuzztime=10s
	$(GO) test ./internal/render -run='^$$' -fuzz=FuzzSanitize -fuzztime=10s

clean:
	rm -f golab
	rm -rf dist
