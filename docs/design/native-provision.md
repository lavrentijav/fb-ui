# Native provisioning

Status: in progress 2026-11-02, branch `native`. Starts with the step that
changes nothing: an SSH preflight that tells the operator what a bare server
is before anything is installed.

## Design

A master inspects a server over SSH and reports what the install may rely on.
Credentials travel in one request and are never stored; the host key is
returned once so the operator can pin it for the steps that do change the
server.

`POST /provision/preflight` (master admin API, loopback only — it takes
credentials):

```json
{"host":"1.2.3.4","port":22,"user":"root",
 "auth":{"kind":"password","password":"…"},
 "hostKey":"SHA256:…"}
```

`auth.kind` is `password` or `key` (a PEM, optionally with `passphrase`).
`hostKey` empty accepts any key and returns the fingerprint — that is the
trust-on-first-use step; a set value must match or the call fails before any
command runs.

Answer (`internal/native/provision.Result`):

| Field | Meaning |
|---|---|
| `hostKey` | `SHA256:…` of the server's key, to pin in the next call |
| `os`, `osId` | `/etc/os-release` `PRETTY_NAME` and `ID` |
| `arch` | `amd64`, `arm64`, `arm`, `386` from `uname -m` |
| `root` | the login user is uid 0 |
| `systemd` | `systemctl` is present |
| `listeners` | TCP ports in use (`ss -ltnH`, else `netstat -ltn`) |
| `portsFree` | whether 80, 443 and 2053 are free |
| `diskFreeBytes` | free space on `/` |
| `panelOnHost`, `agentOnHost` | an `x-ui`/`fbnode` binary is already there |
| `warnings` | non-root user, port 443 taken, existing panel, <512 MB free |

The probe is one shell script with `@@`-marked sections, so a missing tool
never aborts it; parsing lives in `parse` and is unit-tested.

## Tests

`internal/native/provision` runs the real SSH client against an in-process
`sshd` (`golang.org/x/crypto/ssh`) that executes the probe on the test
machine: facts, host-key capture, pinning, a wrong pin, a wrong password, key
parsing and bad input.

## Install

`POST /provision/install` (master admin API, loopback only) does the first
change on the server, in one call:

```json
{"host":"1.2.3.4","port":22,"user":"root",
 "auth":{"kind":"password","password":"…"},"hostKey":"SHA256:…",
 "node":"ru4","masters":{"ru2":"tls://ru2.m.<zone>:443"},
 "dir":"/etc/fbnode","bin":"/usr/local/bin/fbnode",
 "unit":"/etc/systemd/system/fbnode.service",
 "xrayBin":"/usr/local/x-ui/bin/xray-linux-amd64","wait":"30s"}
```

The master, in order: refuses without a configured `NodeBinary` and fleet CA;
mints a one-time enrollment secret and redeems it **itself** (the identity is
created on the master, which already holds the CA key); then over SSH

1. checks `<xrayBin>` is executable and the login user is root — nothing is
   changed if either fails (the core is a prerequisite, installed separately);
2. creates the directories, then uploads the agent binary in base64 chunks
   (96 KiB per command, under Linux' argument limit) and verifies its
   SHA-256, so a truncated transfer never reaches `systemctl`;
3. runs `<bin> enroll` once to prove the file is an executable agent of the
   server's architecture (a wrong-arch file fails here, not at boot);
4. uploads `ca.pem`, `cert.pem`, `key.pem` (0600), `config.json` (0600) and a
   systemd unit (0644);
5. `daemon-reload`, `enable`, `start`, then polls `is-active` for `wait`.

Any failure after the first write runs the undo list in reverse: `disable
--now`, `daemon-reload`, remove the binary, unit, config and identity. The
answer never contains key material: `{node, binary, unit, sha256,
serviceActive}`.

The install endpoint needs the master to be able to read an `fbnode` build
(`Config.NodeBinary`). The node then enrols as in `native-nodes.md`: the
identity is the same, so it joins the fleet on its first pull.

## Register

`POST /provision/register` turns a role and a public name into a spec, so an
installed node serves with its first pull:

```json
{"node":"ru4","role":"entry","host":"ru.lab.firewalk.test",
 "port":12054,"path":"/ws","cascadeTo":"de2","country":"de","vnet":true,
 "overwrite":false,"wait":"60s"}
```

- `role: "exit"` builds a `cascade` inbound (client `servers`) with a public
  host, and a `VNetHop` so other nodes reach it by name on 443.
- `role: "entry"` builds the `vpn` inbound; with `cascadeTo` it adds an
  `Exit` whose hop is the exit's cascade inbound (found in the exit's spec, so
  register the exit first). Without it the inbound routes `direct`.
- The cascade identity (`servers`) is one UUID per fleet, kept in `fleetid`
  and created on first use, so every exit and entry agree.
- The spec is written through the same path as `PUT /nodes/{node}/spec`,
  which also asks for the node's certificate for the terminated names.
- A spec that already exists is refused (409) unless `overwrite`; an
  `cascadeTo` node that is not registered (or not replicated here yet) is
  waited for up to 15 s, then refused (422) with a clear message.
- `wait` (optional) waits a bounded time for the node's first report and
  answers `reported`.

Answer: `{node, summary, spec, reported}`.

## Next steps

1. ~~Preflight~~ — done.
2. ~~Install + enrol~~ — done (`TestInstallNodeOverSSH`: a master installs onto
   a bare server over real SSH, the started agent joins the fleet and applies
   its spec).
3. ~~Register~~ — done (`TestRegisterBuildsTheFleet`: an exit and an entry
   registered through two masters; the running nodes apply the specs, the
   entry grows `proxy_de` to the exit).
4. DNS record and certificate for the new node's name (the spec asks for the
   certificate; the A record still needs the DNS pool).
5. Rollback of a successful install, and reinstall/upgrade.
