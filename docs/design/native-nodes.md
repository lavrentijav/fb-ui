# Native nodes: what a master gives a node

Status: in progress 2026-09-29, branch `native`. Starts from the smallest
useful node and grows; each step is proven in the network lab first.

## The contract

A master renders a `nodespec.Spec` into a `nodespec.Bundle` and serves it
through `desired` (stamped, hashed). The node (`fbnode`) applies it with
`agent.BundleApplier`: Xray first, then the edge, so the edge never points at
an inbound that is not listening yet.

| Spec | Becomes on the node |
|---|---|
| `Inbounds` (VLESS, ws/xhttp, loopback) | Xray inbounds; with `Host`, an edge HTTP route `Host`+`Path` → the inbound and a handoff SNI route for `Host` |
| `Route` of an inbound | routing rule: `direct`, `block` or `proxy_<country>` |
| `Exits` | `proxy_<country>` outbound: VLESS over ws/xhttp + TLS to the exit node's cascade inbound |
| `Reverse` `http` | edge terminates TLS for the name and proxies Host+path to a local/remote URL |
| `Reverse` `tcp` | edge splices TLS untouched by SNI (mail, other TLS services), optional PROXY v1/v2 |
| `Certs` | certificates the edge serves, picked by SNI (exact or `*.zone`) |
| `DirectAllow` | private CIDRs `direct` may reach (Xray blocks private targets by default) |
| always | `direct`, `block`, stats API on 127.0.0.1:10085, per-user traffic counters |

Inbounds behind the edge trust `X-Edge-Proxy` in `trustedXForwardedFor`, so
Xray sees the client's real address (IP limits, fail2ban), not 127.0.0.1.

## Network lab (`tools/netlab/run.sh`)

Unprivileged user+net+mount namespace, nothing changes on the host:
bridge `10.0.0.1/16`, one namespace per machine with its own address, every
node on :443, names from a lab `/etc/hosts`, certificates from a lab CA that
every process trusts through `SSL_CERT_FILE` (like a public CA).

| Host | Address | Names |
|---|---|---|
| ru4 (entry) | 10.0.2.4 | ru4.lab.firewalk.test, ru.lab.firewalk.test |
| de2 (exit) | 10.0.3.2 | de2.lab.firewalk.test |
| web (a site) | 10.0.8.1 | web.lab.test, mail.lab.firewalk.test |
| client | 10.0.9.1 | — |

`TestCascadeThroughEdges`: client → ru4:443 (edge, TLS, WS → Xray,
`proxy_de`) → de2:443 (edge, WS → Xray, `direct`) → site sees de2's
address; `direct` exits from ru4; `block` goes nowhere; HTTP and TCP reverse;
unknown SNI refused; traffic counted on both nodes.

Found by the lab, now handled:
- clients with a Chrome fingerprint (Xray default, v2rayNG) refuse Ed25519
  certificates — public-facing TLS must be ECDSA/RSA (as Let's Encrypt
  issues); Ed25519 stays for the fleet's internal mTLS only;
- Xray 26 `freedom` blackholes private destinations unless `finalRules`
  allow them (`DirectAllow`);
- without `trustedXForwardedFor` Xray logs every client as 127.0.0.1.

## Next steps (in order)

1. Masters in the lab (10.0.1.x): real `fbnode` processes enroll, pick the
   nearest master, pull bundles, report traffic; kill a master mid-run.
2. **Group balancer.** Nodes of one group (e.g. exits `de1`, `de2`) behind
   one `proxy_de`: an `Exit` with several hops renders one outbound per hop
   plus an Xray balancer `proxy_de` (observatory probing through each hop;
   strategy leastPing, fallback to the next alive). Routing rules then use
   `balancerTag`. Entry side: a group's shared name (`ru.fleet.example`) is a
   DNS pool of its members; the master drops a member in maintenance or
   down from the pool. Lab test: stop de2, traffic moves to de1.
3. Drain/maintenance through the bundle (edge refuses new tunnels).
4. Certificates from the masters' ACME (group wildcard + per-node, see the
   node-groups design), renewal pushed as a new bundle.
5. Subscriptions served by masters, pointing at group names.
