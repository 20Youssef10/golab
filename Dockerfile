# syntax=docker/dockerfile:1.7
# Multi-stage, CGO-free build. The runtime stage is a static distroless image
# with both binaries and the two Agent Skills; no shell and no package manager.
#
# Smoke test (also wired into .github/workflows/docker.yml):
#   docker build --target verify -t golab-verify .
#   docker build -t golab .
#   docker run --rm golab --help
#   docker run --rm --entrypoint /golab-mcp golab --help
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
# Module cache first so source edits do not invalidate go mod download.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN set -eu; \
    export CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH"; \
    go build -trimpath -ldflags='-s -w' -o /out/golab ./cmd/golab; \
    go build -trimpath -ldflags='-s -w' -o /out/golab-mcp ./cmd/golab-mcp

# Verification stage: runs both binaries and checks the skill payload without
# needing a shell in the final image. Used by CI with --target verify.
FROM alpine:3.22 AS verify
COPY --from=builder /out/golab /out/golab-mcp /usr/local/bin/
COPY .agents/skills /usr/share/golab/skills
RUN /usr/local/bin/golab --help >/dev/null \
 && /usr/local/bin/golab-mcp --help >/dev/null 2>&1 \
 && test -f /usr/share/golab/skills/golab-operations/SKILL.md \
 && test -f /usr/share/golab/skills/colab-mcp/SKILL.md

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=builder /out/golab /golab
COPY --from=builder /out/golab-mcp /golab-mcp
COPY .agents/skills /usr/share/golab/skills
ENV GOLAB_SKILLS_DIR=/usr/share/golab/skills
USER nonroot:nonroot
ENTRYPOINT ["/golab"]
