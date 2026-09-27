#!/usr/bin/env python3
"""Move an RU cascade entry onto the shared 443 port, next to its old ports.

Run on the host itself as root. For every public VLESS WS+TLS entry inbound
(one per exit country) it adds a loopback twin with a per-country path, gives
the twin the same clients and the same cascade routing, and routes
<shared domain>/ws-<cc> to it through the panel's edge. Old ports keep
working untouched. Idempotent; without --apply it only prints the plan.

usage: ru-entry-443.py --shared ru.walk.home.example --self ru4.walk.home.example [--apply]
"""
import argparse
import json
import os
import re
import secrets
import ssl
import subprocess
import sys
import urllib.parse
import urllib.request

COUNTRIES = {"Герм": "de", "Финл": "fi", "Польш": "pl", "Нидерл": "nl", "Germ": "de", "Finl": "fi", "Pol": "pl", "Neth": "nl"}
TWIN_OFFSET = 10000
DECOY_PORT = 8443
TRUST = "X-Edge-Proxy"


def sh(cmd):
    return subprocess.run(cmd, shell=True, check=True, capture_output=True, text=True).stdout


class Panel:
    def __init__(self):
        tokf = "/root/.fbui-api"
        if not os.path.exists(tokf) or os.path.getsize(tokf) == 0:
            out = sh("/usr/local/x-ui/x-ui setting -getApiToken true 2>/dev/null")
            tok = re.search(r"apiToken: (\S+)", out).group(1)
            old = os.umask(0o077)
            open(tokf, "w").write(tok)
            os.umask(old)
        self.token = open(tokf).read().strip()
        show = sh("/usr/local/x-ui/x-ui setting -show true 2>/dev/null")
        self.port = re.search(r"^port: *(\d+)", show, re.M).group(1)
        self.base = re.search(r"webBasePath: *(\S+)", show).group(1).rstrip("/")
        self.ctx = ssl._create_unverified_context()  # loopback to our own panel

    def call(self, method, ep, body=None, form=None):
        url = f"https://127.0.0.1:{self.port}{self.base}/panel/api/{ep}"
        headers = {"Authorization": "Bearer " + self.token}
        data = None
        if form is not None:
            data = urllib.parse.urlencode(form).encode()
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        elif body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        r = json.load(urllib.request.urlopen(req, timeout=60, context=self.ctx))
        if not r.get("success"):
            raise SystemExit(f"{method} {ep}: {r.get('msg')}")
        return r.get("obj")


def J(x):
    return x if isinstance(x, (dict, list)) else json.loads(x or "{}")


def country(remark):
    for k, v in COUNTRIES.items():
        if k in remark:
            return v
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--shared", required=True, help="shared entry domain, e.g. ru.walk.home.example")
    ap.add_argument("--self", dest="me", required=True, help="this host's own name, e.g. ru4.walk.home.example")
    ap.add_argument("--apply", action="store_true")
    ap.add_argument("--edge-off", action="store_true", help="prepare everything but leave 443 to whoever holds it now")
    ap.add_argument("--decoy", default="www.microsoft.com:443", help="Reality target strangers are handed to")
    a = ap.parse_args()
    p = Panel()
    host = a.me
    # A master also lists its nodes' inbounds; only this host's own count here.
    inbounds = [i for i in p.call("GET", "inbounds/list") if not i.get("nodeId")]
    by_port = {i["port"]: i for i in inbounds}
    plan = []
    for ib in inbounds:
        st = J(ib["streamSettings"])
        if ib["protocol"] != "vless" or st.get("network") != "ws" or st.get("security") != "tls" or ib["listen"] not in ("", "0.0.0.0", "::"):
            continue
        cc = country(ib["remark"])
        if not cc:
            print(f"skip {ib['port']} {ib['remark']!r}: no country in the remark")
            continue
        plan.append((ib, cc, ib["port"] + TWIN_OFFSET))
    if not plan:
        sys.exit("no public VLESS WS+TLS entry inbounds found")
    for ib, cc, tp in plan:
        emails = [c["email"] for c in J(ib["settings"]).get("clients", [])]
        state = "exists" if tp in by_port else "create"
        print(f"{ib['port']} {ib['remark']} -> 127.0.0.1:{tp} /ws-{cc} ({len(emails)} clients) [{state}]")
    print(f"edge: SNI {a.shared} and this panel's name -> panel; unknown SNI -> Reality decoy 127.0.0.1:{DECOY_PORT} ({a.decoy})")
    if not a.apply:
        print("dry run; pass --apply")
        return

    twins = {}
    for ib, cc, tp in plan:
        twin = by_port.get(tp)
        if twin is None:
            st = {"network": "ws", "security": "none", "wsSettings": {"path": f"/ws-{cc}", "host": "", "headers": {}},
                  "sockopt": {"trustedXForwardedFor": [TRUST]}}
            twin = p.call("POST", "inbounds/add", {
                "remark": f"{ib['remark']} · 443", "enable": ib["enable"], "listen": "127.0.0.1", "port": tp,
                "protocol": "vless", "settings": json.dumps({"clients": [], "decryption": "none", "fallbacks": []}),
                "streamSettings": json.dumps(st), "sniffing": json.dumps(J(ib["sniffing"])),
            })
            print(f"created {twin['tag']}")
        twins[ib["tag"]] = twin
        emails = [c["email"] for c in J(ib["settings"]).get("clients", [])]
        if emails:
            res = p.call("POST", "clients/bulkAttach", {"emails": emails, "inboundIds": [twin["id"]]})
            print(f"  attached to {twin['tag']}: {json.dumps(res)[:160]}")

    cur = p.call("POST", "xray/")
    cur = J(cur)
    tpl = J(cur["xraySetting"])
    changed = False
    for rule in tpl.get("routing", {}).get("rules", []):
        tags = rule.get("inboundTag") or []
        for old, twin in twins.items():
            if old in tags and twin["tag"] not in tags:
                tags.append(twin["tag"])
                changed = True
    if changed:
        p.call("POST", "xray/update", form={"xraySetting": json.dumps(tpl, ensure_ascii=False),
                                            "outboundTestUrl": cur.get("outboundTestUrl", "")})
        print("routing: twins follow their originals' cascade rules")

    if DECOY_PORT not in by_port:
        k = p.call("GET", "server/getNewX25519Cert")
        name = a.decoy.split(":")[0]
        st = {"network": "tcp", "security": "reality",
              "tcpSettings": {"acceptProxyProtocol": True, "header": {"type": "none"}},
              "realitySettings": {"show": False, "xver": 0, "target": a.decoy, "serverNames": [name],
                                  "privateKey": k["privateKey"], "shortIds": [secrets.token_hex(8)],
                                  "settings": {"publicKey": k["publicKey"], "fingerprint": "chrome", "serverName": "", "spiderX": "/"}}}
        d = p.call("POST", "inbounds/add", {
            "remark": "edge decoy (Reality)", "enable": True, "listen": "127.0.0.1", "port": DECOY_PORT, "protocol": "vless",
            "settings": json.dumps({"clients": [], "decryption": "none", "fallbacks": []}),
            "streamSettings": json.dumps(st), "sniffing": json.dumps({"enabled": False}),
        })
        print(f"created decoy {d['tag']}")

    have = {(r["kind"], r["match"], r["pathPrefix"]) for r in p.call("GET", "edge/routes")}
    wanted = [{"name": "panel", "kind": "sni", "enable": True, "match": host, "action": "panel"},
              {"name": "shared entry", "kind": "sni", "enable": True, "match": a.shared, "action": "panel"}]
    for ib, cc, tp in plan:
        wanted.append({"name": f"{cc} cascade", "kind": "http", "enable": True, "match": a.shared,
                       "pathPrefix": f"/ws-{cc}", "target": f"http://127.0.0.1:{tp}"})
    for r in wanted:
        if (r["kind"], r["match"], r.get("pathPrefix", "")) not in have:
            p.call("POST", "edge/routes/save", r)
            print(f"edge route {r['name']}")
    p.call("POST", "edge/settings", {"enable": not a.edge_off, "listen": ":443", "defaultAction": "tcp",
                                      "defaultTarget": f"127.0.0.1:{DECOY_PORT}", "defaultProxyProtocol": 2})
    print("edge:", json.dumps(p.call("GET", "edge/status")))
    print("targets:", json.dumps(p.call("POST", "edge/check")))


if __name__ == "__main__":
    main()
