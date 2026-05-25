# syntax=docker/dockerfile:1.7

# ─── Stage 1: build Caddy with the two plugins ────────────────────────────
FROM caddy:2-builder AS caddy-builder
RUN xcaddy build \
    --with github.com/tailscale/caddy-tailscale \
    --with github.com/caddyserver/replace-response

# ─── Stage 2: build the orchestrator ──────────────────────────────────────
FROM golang:1.22-bookworm AS orchestrator-builder
WORKDIR /src
# Cache modules first.
COPY orchestrator/go.mod orchestrator/go.sum* ./
RUN go mod download
COPY orchestrator/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/orchestrator ./cmd/orchestrator

# ─── Stage 3: runtime image ───────────────────────────────────────────────
FROM debian:stable-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tini \
 && rm -rf /var/lib/apt/lists/*

COPY --from=caddy-builder /usr/bin/caddy /usr/local/bin/caddy
COPY --from=orchestrator-builder /out/orchestrator /usr/local/bin/orchestrator
COPY caddy/bootstrap.json /etc/bridge/caddy-bootstrap.json

ENV BRIDGE_CONFIG=/etc/bridge/config.yml \
    CADDY_BOOTSTRAP=/etc/bridge/caddy-bootstrap.json

VOLUME /var/lib/bridge

# tini is PID 1 so SIGTERM/SIGINT/SIGHUP propagate cleanly to orchestrator,
# which in turn forwards lifecycle signals to its Caddy child.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/orchestrator"]
