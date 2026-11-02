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

## Next steps

1. ~~Preflight~~ — done.
2. Install: SSH, drop the `fbnode` binary for the server's arch, write
   `systemd` unit and config, then enrol (see `native-nodes.md`, Enrollment).
3. Register: create the node's spec from a template after it reports once.
4. Rollback: remove the unit, binary and any DNS record on failure.
