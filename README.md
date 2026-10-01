[English](/README.md) | [Русский](/README.ru_RU.md)

# fb-ui

**fb-ui** is a control panel for a fleet of [Xray-core](https://github.com/XTLS/Xray-core)
servers, built to run many servers the way a small provider would: one place to
manage nodes, cascades between them, certificates and DNS, with configuration
changes applied without dropping connections.

It started as a fork of [3x-ui](https://github.com/MHSanaei/3x-ui) and keeps
everything 3x-ui does — inbounds, clients, traffic accounting, subscriptions,
the Telegram bot, the API — and adds the fleet layer on top.

> [!IMPORTANT]
> fb-ui is under active development. Use it on servers you own and in line with
> the laws that apply to you.

## What fb-ui adds to 3x-ui

**Fleet and nodes**
- A master panel manages its nodes; any server becomes a node **over SSH from
  the panel** (install, certificate, registration in one wizard).
- **Maintenance mode**: a node stops taking new connections, lets open ones
  finish and reports when it is drained.
- A node can be **handed over to another master** without touching it.
- **Templates**: save a set of inbounds once and stamp it onto other servers.

**Cascades and routing**
- Entry → exit **cascades** between nodes, drawn on a **node-graph editor**;
  the panel generates the outbound each hop dials.
- **Filter chains** (matcher lists, can follow a published list by URL) turned
  into Xray routing, delivered from the master to its nodes.
- Subscriptions hand out a cascade entry **once**, not once per node.

**One public port**
- An **edge** on 443 shares the port between the panel, inbounds and ordinary
  websites (by SNI and path), and passes the real client address on to Xray.

**Certificates and DNS**
- **DNS accounts** (Cloudflare, Technitium) set up once; zones and subdomains
  are discovered automatically.
- **ACME** certificates (DNS-01 and HTTP-01) issued, renewed and watched by
  the panel; node certificates ordered on the master and pushed to the nodes;
  one certificate can be shared by several nodes.
- **Domain moves**: plan and apply a move of the whole fleet to (or a copy
  into) a new base domain.

**Panels together**
- Peer panels with health probing, **signed subscriptions** that advertise
  fallback panels, and a shared table of which panel leads.

## Branches

| Branch | What it is |
| --- | --- |
| `dev` | The panel as it runs today: 3x-ui plus the features above. |
| `native` | The next generation, in development (below). |

### `native`: multi-master and a light node agent

The `native` branch grows a new core inside the same repository, step by step:

- **Several masters, no single point of failure.** Masters keep an operation
  log replicated over an embedded NATS supercluster (JetStream mirrors), each
  with its own PostgreSQL copy; writes merge last-writer-wins. Either side of a
  network split keeps working.
- **`fbnode`** — a small static agent (≈7.5 MB, ≈11 MB RAM) instead of a full
  panel on every node. It picks the nearest master by latency, throughput and
  load, pulls its desired state, and ships reports back.
- **Live Xray changes** through Xray's API: users, inbounds and routing change
  without restarting Xray, so open connections survive.
- **Certificates and DNS by the masters**: ACME issued and renewed by whichever
  master holds the lease; `master.<zone>` is a DNS pool of the live masters.
- **Network lab**: tests run real masters, nodes, Xray and an ACME server on a
  virtual network with real-looking addresses.
- **In progress**: user virtual networks inside the tunnel (members reach each
  other's devices by internal address) and exposing a user's own ports by
  domain. Design: [`docs/design/native-vnet.md`](https://github.com/lavrentijav/fb-ui/blob/native/docs/design/native-vnet.md).

Design documents live in `docs/design/` on the `native` branch.

## Building

There are no fb-ui release builds yet; the install script still installs
upstream 3x-ui. Build from source:

```bash
make build                          # frontend (Node.js) + Go binary
deploy/build/release-static.sh      # static musl build, as deployed on the fleet
```

Requirements: Go 1.27, Node.js for the frontend, a C compiler (SQLite driver).
See [CONTRIBUTING.md](CONTRIBUTING.md) for tests and the full local gate
(`make verify`).

Do not use the panel's built-in "update panel" on an fb-ui server: it installs
upstream 3x-ui over it.

## Everything from 3x-ui

Protocols (VLESS, VMess, Trojan, Shadowsocks, WireGuard, Hysteria2, TUIC,
MTProto, AmneziaWG, HTTP, SOCKS, Tunnel, TUN), transports (TCP, mKCP, WebSocket,
gRPC, HTTPUpgrade, XHTTP) with TLS and REALITY, per-client quotas, expiry and IP
limits, traffic statistics, subscriptions (raw, JSON, Clash), Telegram bot,
REST API with Swagger, SQLite or PostgreSQL, 13 UI languages. The upstream
documentation applies: [3x-ui wiki](https://github.com/MHSanaei/3x-ui/wiki).

## License and credits

GPL-3.0, like 3x-ui. Built on [3x-ui](https://github.com/MHSanaei/3x-ui) by
MHSanaei and contributors, and on [Xray-core](https://github.com/XTLS/Xray-core).
