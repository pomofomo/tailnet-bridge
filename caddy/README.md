# tailnet-bridge

Per-user L7 reverse proxy bridging a Tailscale personal tailnet to one or
more community tailnets. See [SPEC.md](./SPEC.md) for the design and
[CLAUDE.md](./CLAUDE.md) for the implementation overview.

## Quick start

1. Copy `config.example.yml` to `config.yml` and edit it.
2. Export the auth keys referenced by `auth_key_env` entries in your shell
   or a `.env` file.
3. `docker compose up -d`.
4. Visit `https://<prefix><service>.<your-personal-tailnet>.ts.net` from any
   device on your personal tailnet.

## Reload

```
docker compose kill -s HUP bridge
```

forces an immediate re-poll of every community directory. Editing
`config.yml` requires a container restart (`docker compose restart bridge`).

## Status

```
docker compose exec bridge wget -qO- http://127.0.0.1:8081/__bridge_status
```

returns per-community health (last successful poll, last error, etag).
