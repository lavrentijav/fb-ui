#!/usr/bin/env python3
"""Remap native inbound loopback ports (+10000, to sit beside x-ui) and,
for the RU entry nodes, add the /walksub + /frvpn reverse routes.

Reads the admin token from the master log itself; takes the node name and an
optional --reverse flag.

Usage: remap_node.py <node> [--reverse]
"""
import json
import re
import sys
import urllib.request

API = "http://127.0.0.1:18081"
LOG = "/opt/fbui-staging/master.log"


def token():
    for line in open(LOG, errors="ignore"):
        m = re.search(r"first admin token \(shown once\): ([0-9a-f]+)", line)
        if m:
            return m.group(1)
    raise SystemExit("admin token not found in " + LOG)


def req(tok, method, path, body=None):
    r = urllib.request.Request(API + path, method=method)
    r.add_header("Authorization", "Bearer " + tok)
    if body is not None:
        r.add_header("Content-Type", "application/json")
        r.data = json.dumps(body).encode()
    raw = urllib.request.urlopen(r).read().decode()
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return raw


def remap(tok, node, reverse):
    ibs = req(tok, "GET", "/inbounds/" + node)
    for ib in ibs:
        p = int(ib.get("port", 0))
        if p >= 18000:
            continue  # already remapped (original ports are <= 12443)
        new = p + 10000
        body = {
            "tag": ib["tag"],
            "port": new,
            "network": ib.get("network", "ws"),
            "path": ib.get("path", "/"),
            "host": ib.get("host", ""),
            "route": ib.get("route", "direct"),
        }
        req(tok, "PUT", "/inbounds/%s/%s" % (node, ib["tag"]), body)
        print("  remap %s/%s: %d -> %d" % (node, ib["tag"], p, new))

    if reverse:
        ibs = req(tok, "GET", "/inbounds/" + node)
        spec = {
            "node": node,
            "inbounds": ibs,
            "reverse": [
                {"kind": "http", "sni": "ru.firewalk.top", "path": "/walksub/", "target": "https://sub.firewalk.top:1443", "host": "sub.firewalk.top"},
                {"kind": "http", "sni": "ru.firewalk.top", "path": "/frvpn/", "target": "https://sub.firewalk.top:1443", "host": "sub.firewalk.top"},
            ],
        }
        req(tok, "PUT", "/nodes/%s/spec" % node, spec)
        print("  reverse routes set on %s" % node)


if __name__ == "__main__":
    node = sys.argv[1]
    reverse = "--reverse" in sys.argv
    remap(token(), node, reverse)
