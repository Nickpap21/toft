# syntax=docker/dockerfile:1
# The builder runs on the build machine and cross-compiles for the target
# platform (release images are amd64 and arm64, D-61).
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:30b1e546a9c7fa3b747c2bdba3a0c7bdf3a02ebb4c5b8405d2f948d7e8aac4ec AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod \
    go mod download

COPY . .
ARG TARGETOS TARGETARCH
# The release tag (D-61); "dev" when not given.
ARG TOFT_VERSION=dev
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    export CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      LDFLAGS="-X $(go list -m)/pkg/buildinfo.Version=${TOFT_VERSION}" && \
    go build -ldflags "$LDFLAGS" -o /server ./cmd/server/... && \
    go build -ldflags "$LDFLAGS" -o /toft ./cmd/toft && \
    go build -ldflags "$LDFLAGS" -o /toft-git ./cmd/toft-git

# The node agent for both architectures (D-106), whatever this image's: the
# panel serves it to servers. The release job builds this stage on its own
# (--target agent-bins) and signs what it outputs; the image then carries
# those signed binaries from agent-dist/.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:30b1e546a9c7fa3b747c2bdba3a0c7bdf3a02ebb4c5b8405d2f948d7e8aac4ec AS agent-build
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod \
    go mod download
COPY . .
ARG TOFT_VERSION=dev
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -buildvcs=false \
        -ldflags "-s -w -X $(go list -m)/pkg/buildinfo.Version=${TOFT_VERSION}" \
        -o /agent/toft-agent-linux-$arch ./cmd/toft-agent || exit 1; \
    done

FROM scratch AS agent-bins
COPY --from=agent-build /agent/ /

# alpine 3.24 for git 2.54, the oldest toft-git runs on (D-71).
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
# docker-cli-compose: deploys to the control-plane server run `docker compose`.
RUN apk add --no-cache ca-certificates tzdata git docker-cli docker-cli-compose
# The paas-git container runs this image as git (uid 10001): the
# repositories' volume and the socket's directory start out owned by it.
RUN adduser -D -H -u 10001 -s /sbin/nologin git && \
    install -d -o 10001 -g 10001 -m 0750 /var/lib/toft/git /run/toft-git
ARG TOFT_VERSION=dev
LABEL org.opencontainers.image.version=$TOFT_VERSION
WORKDIR /app
COPY --from=builder /server .
# The CLI, for the panel host: install.sh copies it out (D-61).
COPY --from=builder /toft /usr/local/bin/toft
# git for Toft's repositories, alone in the paas-git container (D-71), and
# its push hooks (D-75): pushes run with core.hooksPath at these links.
COPY --from=builder /toft-git /usr/local/bin/toft-git
RUN install -d /usr/local/lib/toft-git/hooks && \
    ln -s /usr/local/bin/toft-git /usr/local/lib/toft-git/hooks/pre-receive && \
    ln -s /usr/local/bin/toft-git /usr/local/lib/toft-git/hooks/post-receive
# The node agent the panel serves (TOFT_AGENT_DIR, D-106): built above, then
# the release's signed binaries and their .sig over them when agent-dist/
# has them (a local build has only its README).
COPY --from=agent-build /agent/ /usr/share/toft/agent/
COPY agent-dist/ /usr/share/toft/agent/
RUN rm -f /usr/share/toft/agent/README.md
# The release bundle `toft upgrade` installs from this image (D-61, D-63):
# the stack, the panel's Traefik configuration, the migrations and the host
# units. A release is its images and nothing else.
COPY --from=bundle docker-compose.yml /usr/share/toft/bundle/docker-compose.yml
COPY --from=bundle docker-compose.git.yml /usr/share/toft/bundle/docker-compose.git.yml
COPY --from=bundle traefik/dynamic/dynamic.yml /usr/share/toft/bundle/traefik/dynamic/dynamic.yml
COPY --from=bundle install.sh /usr/share/toft/bundle/install.sh
COPY db/migrations/schema.sql db/migrations/premigrate.sql db/migrations/contract.sql db/migrations/atlas.hcl /usr/share/toft/bundle/migrations/
COPY internal/server/firewall.nft internal/server/toft-firewall.service internal/server/toft-backup.sh internal/server/toft-backup.service internal/server/toft-backup.timer cmd/toft/toft-release-check.service cmd/toft/toft-release-check.timer /usr/share/toft/bundle/host/
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/health 2>/dev/null || exit 1
CMD ["./server"]
