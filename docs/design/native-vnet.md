# Native virtual networks and port exposure

Status: spec, 2026-09-30, branch `native`. Agreed with the owner in a design
session; nothing built yet. Builds on native-nodes.md (edge, cascades, live
Xray updates) and native-multimaster.md (op log, masters).

## Goal

A VPN user can:

1. **Share a virtual network** with other users — like Hamachi/Radmin: members
   reach each other's devices on any port by an internal address, for games and
   home services, wherever each of them is connected.
2. **Expose their own ports to the internet by domain** —
   `vasya.u.firewalk.top` — and decide which ports are open.

All of it runs inside the Xray tunnel the user already has; no second client.

## Decisions taken

| Question | Decision | Why |
|---|---|---|
| Can Xray carry connections server → client? | Yes: VLESS reverse (`"reverse": {"tag": …}` on the server's user and on the client's outbound) | Proven in the lab (`TestReverseReachesThePlayer`): internet host → node:25565 → player's `127.0.0.1:25565`, TCP and UDP, tunnel up ~2.7 s after the client connects, port dead once the client leaves |
| Where a network lives | On every node at once | Members connect wherever they are |
| Transport between nodes | The existing VLESS cascades, no WireGuard | WireGuard is cut by DPI on RU ↔ abroad links; VLESS cascades already cross them |
| Who reaches exposed ports | The owner decides per port; closed by default | Owner's choice |
| Clients | Stock clients through a JSON subscription (full Xray config), later our own app/bot conveniences | Both |
| Networks per user | At most **one**, own or someone else's | Owner's rule |
| IPv6 | Not relied on | Weak support in RU |
| Routing by the name a client typed | Only where the name travels in the connection: TLS (SNI), HTTP (`Host`), Minecraft Java (handshake). DNS cannot do it: the query comes from the client's resolver, not the client | Everything else gets an allocated port |

## Architecture

```
member A ──VLESS(+reverse u-A)──> node X ──cascade──> node Y <──VLESS(+reverse u-B)── member B
internet ──443/25565/allocated port──> any pool node (edge) ──> Xray ──> (cascade) ──> u-B ──> B's 127.0.0.1:port
```

- **Masters** keep networks, members, invites, names and exposes in the op log
  like all other state, serve the API and build each user's JSON
  subscription.
- **Nodes** hold each connected user's reverse tunnel (tag `u-<id>`), report
  who is connected, and route by the fleet's presence map. Route changes go
  through `xray api` (no restart, native-nodes.md step 5).
- **Edge** (ours, every pool node) takes all traffic from the internet:
  shared ports by name, allocated ports by number, and hands it to the local
  Xray, which carries it to the owner's address like in-network traffic.
- **DNS**: one wildcard, `*.u.<zone>`, pointing at the pool of nodes that take
  exposes. No per-user records; a user moving between nodes changes nothing
  in DNS.

## Addresses

Every user gets one permanent internal address from `100.64.0.0/10` (as
Tailscale does): it rarely clashes with home LANs and holds millions of users.
Members of a network reach each other by these addresses. Allocation is by the
master creating the user; the address never changes or is reused while the
user exists.

## Data (op log tables)

| Table | Key | Fields |
|---|---|---|
| `vaddr` | user | `ip` |
| `uname` | name | `user` — `vasya` → `vasya.u.<zone>`; unique, chosen by the user |
| `vnet` | network id | `title`, `owner` |
| `vnet_member` | user | `network`, `role` (`owner`/`admin`/`member`) — keyed by user: one network per user holds by construction |
| `vnet_invite` | code | `network`, `by`, `expires`, `uses` |
| `expose` | id | `user`, `kind` (`https`, `tls`, `minecraft`, `tcp`, `udp`), `port` (the device's), `target` (`127.0.0.1` or a LAN address), `public` (allocated port for `tcp`/`udp`) |
| `expose_port` | public port | `expose` — the fleet-wide allocation, unique by key |

Concurrent writes on two masters are the op log's usual last-writer-wins;
the two uniqueness points (`uname`, `expose_port`) are claimed by a key and
re-checked after the write replicates (the lease pattern of `certs`), and the
loser gets a "taken" error.

## Rights

- Inside a network it is a LAN: members reach each other's addresses on any
  port. The **source** node enforces "same network" (by the user the
  connection came in as and the destination address); the destination node
  trusts its cascade, as today.
- From the internet nothing is reachable unless the owner exposes it, and only
  the ports exposed.
- Owner: delete the network, transfer it, change roles. Owner and admins:
  invite, remove members. Anyone: leave. Creating or joining is refused while
  in a network.

## API

On the masters, under `master.<zone>/api/v1`, JSON, messages in Russian so the
bot can show them as they are. A user authenticates with their subscription
token; the bot and the panel use a service token acting for a user.

| Method | Path | Does |
|---|---|---|
| GET | `/me` | name, address, network, exposes, current node |
| PUT | `/me/name` | choose the name |
| POST | `/network` | create (refused if in a network) |
| GET | `/network` | members: name, address, role, online, node |
| DELETE | `/network` | delete (owner) |
| POST | `/network/leave` | leave |
| POST | `/network/transfer` | give ownership to a member |
| POST | `/network/invites` | `{ttl, uses}` → code and bot link |
| DELETE | `/network/invites/{code}` | revoke |
| POST | `/network/join` | `{code}` (refused if in a network) |
| DELETE | `/network/members/{user}` | remove (owner, admin) |
| PATCH | `/network/members/{user}` | set role (owner) |
| GET | `/exposes` | list |
| POST | `/exposes` | `{kind, port, target}` → the address to share: `vasya.u.<zone>` (shared ports) or `vasya.u.<zone>:40123` (allocated) |
| DELETE | `/exposes/{id}` | close |
| GET | `/sub/<token>` | the client's JSON config (below) |

## Presence

- The node agent asks its Xray every 1–2 s which users hold a reverse tunnel
  and publishes changes on the bus (`presence.<node>`); a full snapshot every
  30 s repairs a lost message; an entry not refreshed for 90 s is gone.
  Presence is operational: it is not written to the op log.
- Masters relay the fleet map to every node; nodes turn it into routing.
- One user on two devices on two nodes: the most recently connected one gets
  incoming traffic.

## Routing on a node

For a network N and a member B at address `ipB`:

- B on this node: `user ∈ members(N) ∧ ip = ipB → u-B`.
- B on node Y: `user ∈ members(N) ∧ ip = ipB → node-Y` (cascade); on Y,
  `inbound = cascade ∧ ip = ipB → u-B`.
- Exposes: the edge dials the local Xray (a loopback SOCKS inbound `expose`)
  for `ipB:port`; `inbound = expose ∧ ip = ipB → u-B` or `→ node-Y`.
- Nothing else routes to member addresses.

The rules change with presence and membership and are applied live
(`adrules` swaps the whole routing at once).

## Edge

- **Shared ports**, every pool node: 443 (TLS: SNI), 80 (HTTP: Host, answered
  with a redirect to https), 25565 (Minecraft Java: the address in the
  handshake packet — a new, small parser). The name gives the user; the edge checks the expose exists and
  dials Xray for `ipB:port`.
  - `https`: the edge terminates TLS with the `*.u.<zone>` wildcard (issued by
    the masters' ACME) and forwards plain HTTP — the user needs no certificate.
  - `tls`: spliced as is, for users with their own certificate.
- **Allocated ports** (`tcp`, `udp`), range 20000–60000, one port per expose
  for the whole fleet, taken on **every** pool node, so moving between nodes
  breaks nothing. The node redirects the range to the edge with iptables
  (`REDIRECT`, original port read back with `SO_ORIGINAL_DST`; UDP by TPROXY);
  the edge maps the port to the expose. No per-port Xray inbounds.
- Every external connection to an expose is reported (source, time, bytes,
  expose) through the node report journal.
- A rate limit on new connections per expose.

## Client (JSON subscription)

On top of the usual VPN profile:

- the VLESS outbound carries `"reverse": {"tag": "from-node"}`;
- incoming from `from-node`: to `127.0.0.1` on the same port (`freedom` with
  `redirect: "127.0.0.1:0"`), or to a LAN device for exposes with a LAN
  target; `finalRules` allow `127.0.0.0/8` and the target LAN addresses
  (Xray 26 blocks private targets otherwise — found in the lab);
- `100.64.0.0/10` routed into the tunnel, TUN mode on, so apps reach members.

Which stock clients apply `reverse` and TUN from a JSON subscription
(v2rayNG, Happ, Hiddify, …) must be checked before this is offered; the list
goes to the user-facing help.

## Limits and abuse

- Hard: one network per user.
- Panel settings with defaults: 16 members per network; 5 exposes per user, 3
  of them allocated `tcp`/`udp`.
- Panel switches: close all of a user's exposes; forbid exposes for a user.
- Exposed traffic leaves from our nodes' addresses: the connection reports are
  what answers an abuse complaint.

## Out of scope

- ICMP and LAN broadcast/multicast (games discovering servers by broadcast
  need the address typed in).
- IPv6.
- Routing the internet-facing name straight to the user's current node
  (fewer hops); a later optimisation, DNS only.
- WireGuard between nodes.

## Tests (network lab, real addresses and Xray)

1. Two members on **different** nodes reach each other, TCP and UDP; a
   non-member cannot.
2. A member moves to another node and is reachable again within seconds.
3. From the internet: `https` (our certificate), Minecraft by name on the
   shared 25565, an allocated TCP and UDP port through **any** pool node.
4. A closed expose stops answering at once; a member who left loses access.
5. The node a member was on dies: access is back after the client
   reconnects.

## Order of work

Depends on users and subscriptions served by masters (native-nodes.md next
step) and the master pool (`master.<zone>`, in progress).

1. Addresses, networks, members, invites; API; presence; in-network routing
   (tests 1, 2, 4, 5).
2. Exposes by name: https/tls/http, Minecraft handshake (test 3, shared part).
3. Allocated ports with the iptables redirect (test 3, allocated part).
4. Limits, reports, panel switches.
