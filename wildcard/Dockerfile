# syntax=docker/dockerfile:1.7

# Both stages need the same Go toolchain that orchestrator/go.mod and
# caddy-plugin/bridgedns/go.mod require. Pinning the same image avoids
# the silent skew the previous Dockerfile suffered from (caddy:2.8-builder
# shipped an older Go than the orchestrator declared).

ARG GO_IMAGE=golang:1.26-bookworm

# ─── Stage 1: build Caddy with the Tailscale plugin + bridgedns ───────────
# Note: replace-response is intentionally NOT included. The wildcard-domain
# variant uses the same canonical hostname on both sides of the bridge, so
# no body rewriting is ever needed (see SPEC §3.2, §5.1).
#
# We use xcaddy directly under a standard golang image rather than the
# caddy:builder image so the Go toolchain stays in lockstep with the
# orchestrator stage and our module's `go` directive.
FROM ${GO_IMAGE} AS caddy-builder
WORKDIR /src
RUN go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
# Copy the local bridgedns plugin source into the builder so xcaddy can
# reference it via --with <module>=<localpath>. The plugin shares the
# same tsnet.Server caddy-tailscale creates, and binds UDP/53 for the
# personal-tailnet Split DNS responder (SPEC §10.1).
COPY caddy-plugin/bridgedns/ /src/bridgedns/
RUN xcaddy build \
    --output /out/caddy \
    --with github.com/tailscale/caddy-tailscale \
    --with github.com/tailnet-bridge/wildcard/caddy-plugin/bridgedns=/src/bridgedns

# ─── Stage 2: build the orchestrator ──────────────────────────────────────
FROM ${GO_IMAGE} AS orchestrator-builder
WORKDIR /src
COPY orchestrator/go.mod orchestrator/go.sum* ./
RUN go mod download
COPY orchestrator/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/orchestrator ./cmd/orchestrator

# ─── Stage 3: runtime image ───────────────────────────────────────────────
FROM debian:stable-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tini \
 && rm -rf /var/lib/apt/lists/*

COPY --from=caddy-builder /out/caddy /usr/local/bin/caddy
COPY --from=orchestrator-builder /out/orchestrator /usr/local/bin/orchestrator
COPY caddy/bootstrap.json /etc/bridge/caddy-bootstrap.json

ENV BRIDGE_CONFIG=/etc/bridge/config.yml \
    CADDY_BOOTSTRAP=/etc/bridge/caddy-bootstrap.json

# tsnet state persists across restarts so nodes keep their identity.
VOLUME /var/lib/bridge
# Wildcard cert/key files for each community, owned and updated by the
# user. The orchestrator only stats and reads.
VOLUME /etc/bridge/certs

# tini is PID 1 so SIGTERM/SIGINT/SIGHUP propagate cleanly to orchestrator,
# which in turn forwards lifecycle signals to its Caddy child.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/orchestrator"]
