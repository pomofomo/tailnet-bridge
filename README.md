# Tailscale Community Bridge

**Private personal networks, shared community services, one-way by construction.**

This repository contains designs and implementations for *community bridges* — small per-user services that let your personal Tailscale network reach shared services on a community Tailscale network, without ever exposing your personal devices to the community.

## The Idea

Tailscale is wonderful for individuals: your laptop, phone, NAS, and home services live on a private mesh network that nobody else can see. It's also wonderful for organizations: a company tailnet lets coworkers reach shared infrastructure as if they were on the same LAN.

But there's an awkward middle ground where Tailscale doesn't have a great native story: **communities of individuals who want to share some services without merging their networks**. A family that wants a shared photo library, wiki, and git server. A hackerspace running shared tools for its members. A neighborhood with shared cameras and a community chat. A small co-op sharing internal apps.

Putting everyone on one big tailnet works, but it dissolves the boundaries people care about. Now your sister's laptop can reach your home NAS. Now the hackerspace can see your personal phone. Now everyone in the co-op is one ACL bug away from your private services. People understandably don't want that.

The structure that actually matches how humans think about this is:

- **You have your own personal network.** It's yours. Only your devices are on it. Nothing else can reach in.
- **The community has its own network** containing the services it shares with its members.
- **You can reach community services** from your personal devices, because you're a member.
- **The community cannot reach you.** Not its admins, not its services, not other members. Membership in a community is a *one-way mirror*.
- **You can be in multiple communities at once**, and they don't see each other or you.
- **Joining or leaving** is a small admin action; it cleanly grants or revokes access without touching anything else.

That's it. That's the model. It's how clubs, families, and informal groups already work in the physical world — being part of the chess club doesn't give the chess club a key to your house.

A community bridge is the small piece of software that makes this work on top of Tailscale. You run it on a machine you control; it joins your personal tailnet and the community's tailnet; it exposes community services to your personal devices and forbids traffic the other direction. Each user runs their own bridge. The community can't reach into it. You can quit a community by deleting your bridge's config for it.

## Why Not Just Use Tailscale's Built-In Features?

Tailscale's machine sharing is genuinely close to this — it gives someone access to a single shared machine, with quarantine ensuring the share is one-way. It's a great fit for some setups and you should consider it first. The bridges in this repo solve cases sharing doesn't cover:

- You want a stable URL for a service that doesn't change as members come and go.
- You're running your own control plane (Headscale) or otherwise need self-hosted glue.
- You want one bridge to expose a community to your devices, rather than every member individually accepting share invitations for every service.
- You want services to be reached by names you control, with certs you control, not per-recipient `*.ts.net` URLs.
- You want to compose multiple communities into one personal tailnet cleanly.

Different implementations in this repo make different tradeoffs around these.

## What's In This Repo

Two implementations of the same idea, with different complexity/capability tradeoffs. Both are specified in detail before they're built — the specs are the primary deliverable here, and the code follows.

### [`caddy/`](./caddy) — The Caddy + MagicDNS Bridge

The simpler of the two. Each community service is exposed on your personal tailnet as its own node, with a name like `smithfamily-wiki.<your-tailnet>.ts.net`. Caddy handles everything: TLS, listening, dialing the community, and rewriting Location headers and cookies so apps mostly Just Work. The community publishes a small JSON directory listing its services, and the bridge polls it to know what to expose.

Best when: you don't want to operate a DNS domain or distribute certs. The community admin just needs to publish a list of services, hand out auth keys, and they're done. Works with most well-behaved self-hosted apps (Gitea, Vaultwarden, Nextcloud, etc.) out of the box. Some apps that hardcode their hostname in JavaScript or JSON APIs may not work cleanly because of the hostname mismatch between what the user types and what the service thinks its name is.

[Read the spec →](./caddy/SPEC.md)

### [`wildcard/`](./wildcard) — The Wildcard Domain Bridge

The more capable of the two, at the cost of some setup work for the community admin. The community owns a DNS subdomain like `ts.example.com`, with the invariant that nothing under it ever resolves on the public internet. Each community gets its own subdomain (`smithfamily.ts.example.com`), and services are reached at `wiki.smithfamily.ts.example.com` — the same name on both sides of the bridge. Because the canonical name matches end to end, no rewriting happens, and even apps with hardcoded URLs in JavaScript or OAuth callbacks work cleanly. The community admin issues a short-lived wildcard TLS cert and distributes it to members on a rotation cadence; cert rotation doubles as the membership cutoff mechanism.

Best when: you want maximum app compatibility and you have a community admin willing to operate a domain and a periodic cert rotation. The trust model — sharing a wildcard cert among members — is safe specifically because the `ts.` subdomain is constrained to tailnet-only resolution, so a leaked key can't be used to impersonate anything outside the tailnet.

[Read the spec →](./wildcard/SPEC.md)

## Shared Design Principles

Both implementations share these principles, which are explained in detail in each spec:

1. **Compose, don't build.** Use Caddy and the existing Tailscale plugin ecosystem rather than building custom proxies.
2. **User sovereignty.** Each user fully owns and operates their own bridge. The community has no access to it.
3. **One-way by construction.** The asymmetric access rule is enforced at multiple layers — by what listeners the bridge opens, by community-side ACLs, by structural choice of which side runs which code.
4. **Failure is visible.** When access is lost, users see a clear error page identifying what's broken and who to contact.
5. **The directory is authoritative.** Communities declare their services in a JSON document; the bridge does not infer or override.
6. **HTTPS everywhere.** All traffic between client, bridge, and upstream is HTTPS, even though WireGuard already encrypts underneath.
7. **Configuration is data, not code.** No per-user templating logic on the community side; no per-service custom code on the bridge side.

## Status

Specifications complete; implementations forthcoming. The specs are designed to be implementable independently, so contributions toward either are welcome.

## Future Directions

Things explicitly out of scope for the first cut of either implementation, but reasonable to add later:

- Headscale support (currently hosted Tailscale only)
- Non-HTTPS protocols (SSH, raw TCP, Postgres)
- Automated cert distribution for the wildcard variant
- A small admin UI for community operators
- Federation between communities

Open an issue if any of these matter to your use case.
