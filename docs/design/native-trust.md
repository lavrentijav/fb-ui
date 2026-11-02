# Fleet trust model

Status: 2026-11-02. Who may issue what, what each key is for, and how a host
proves who it is.

## Keys

| Key | Where | Purpose |
|---|---|---|
| Fleet CA (ed25519 cert + key) | the first master; masters that are allowed to issue | signs node and master certificates |
| Fleet CA certificate only | every other master (from `fbmaster join`) | verifies nodes and peers; **cannot** issue |
| Master certificate (`<name>`) | each master | mTLS identity for the bus gateways and the client port |
| Node certificate (`node.<name>`) | each node (from `fbnode enroll`) | mTLS identity on the client port; the node may only send its own reports |
| Enrollment token (`enr1_<secret>_<caPin>`) | minted on a master, pasted on a node | one-time right to become a node |
| Join token (`mst1_<secret>_<caPin>`) | minted on a master, pasted on a new master | one-time right to become a master |

The token carries the CA's sha256 fingerprint. The joining host checks the CA
it receives against that fingerprint, so a stolen token presented to a
man-in-the-middle cannot substitute another CA. Tokens are stored as sha256
hashes in the op log and are deleted on first use; an expired one is deleted
when it is refused.

## Who can issue

- A master holding the CA **key** can enrol nodes (`POST /enroll/tokens`) and
  join new masters (`POST /master/tokens`).
- A master with only the CA **certificate** starts normally and serves the
  fleet, but its `/enroll` and `/master/join` routes are absent (no CA object
  is loaded), so it cannot create credentials. `fbmaster` logs
  "verifies but does not issue".
- Compromise of a non-issuing master therefore does not let an attacker mint a
  node or master certificate; compromise of an issuing master does, which is
  why their number should be small.

## What a certificate may do

- `node.<name>` is accepted on the client port; the bus checks that a node's
  report stream belongs to its own CN, so a node cannot forge another's
  traffic or presence.
- A master certificate is accepted on the gateways in both directions; any
  fleet member may speak to any other (the op log is replicated to all).
- The admin HTTP surface is not part of the fleet PKI: it uses API tokens and
  panel sessions, and is loopback by default (see `native-prod-plan.md`,
  phase 0).

## Rotation and recovery

- A node's certificate is re-issued by enrolling again; the old certificate
  stays valid until its expiry unless the CA is rotated.
- A master's certificate is re-issued by `fbmaster join` with a fresh token
  (delete `master.pem`/`master-key.pem` first).
- Rotating the fleet CA means re-enrolling every node and rejoining every
  master; it is a planned outage, not an in-place action. No code regenerates
  a CA silently: a CA key without its certificate is refused.

## Pool and DNS runbook

The pool name (`master.<zone>`) is what clients, the panel and the
subscriptions are reached by.

1. Point the name at every master's public address (the masters do this
   themselves once `poolName` and a DNS account are configured; see
   `native-prod-plan.md` phase 2 and `native-node-status.md` for the health
   rules).
2. Each master announces one or more addresses (`publicIPs`, or self-detected).
   An operator can pin the list: `PUT /net/ips {"ips":[...]}`; `{"auto":true}`
   returns to detection.
3. Every master checks every address (`https://<ip>:443/healthz`, SNI = the
   pool name) and publishes a verdict in `iphealth`. An address leaves DNS only
   when a share of the fleet calls it down after repeated failures; the lease
   holder writes the records, and a blocked address is replaced by the next
   live one.
4. Verify: `GET /net/ips` shows announced, detected and failing addresses;
   `dig +short master.<zone>` shows the live set.
5. If every master fails, the pool keeps its last good records — the name never
   goes empty by accident. Check `GET /net/health` and the masters' logs.
