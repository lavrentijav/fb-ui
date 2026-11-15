#!/usr/bin/env python3
"""Restore imported inbound remarks on a native master from the old 3x-ui DB.

The staging import ran before the remark fix, so every native inbound has an
empty Remark and the subscription shows the "<node>-<tag>" default. This maps
each native tag back to the old inbound by (node id, port) and PUTs the remark.

Dry-run by default; --apply performs the PUTs. Only runs on the master host
(reads /etc/x-ui/x-ui.db read-only and the admin token from the master log).
"""

from __future__ import annotations

import argparse
import json
import re
import sqlite3
import subprocess
import sys

OLD_DB = "/etc/x-ui/x-ui.db"
API = "http://127.0.0.1:18081"
MASTER_LOG = "/opt/fbui-staging/master.log"

# native node name -> old x-ui node_id (13=old ru8/dns2, 14=old ru7/dns1)
NODES = {"fi1": 8, "de2": 1, "de3": 12, "nl3": 15, "ru": 13, "ru1": 14}

TAG_RE = re.compile(r"^n(\d+)-in-(\d+)-")


def admin_token() -> str:
    out = subprocess.run(
        ["sh", "-c", f"grep 'first admin token' {MASTER_LOG} | tail -1 | sed 's/.*: //'"],
        capture_output=True, text=True,
    )
    token = out.stdout.strip()
    if not token:
        sys.exit("no admin token found in the master log")
    return token


def old_remarks() -> dict[tuple[int, int], str]:
    con = sqlite3.connect(f"file:{OLD_DB}?mode=ro", uri=True)
    try:
        return {
            (int(nid), int(port)): remark
            for nid, remark, port in con.execute(
                "SELECT node_id, remark, port FROM inbounds WHERE node_id IS NOT NULL"
            )
        }
    finally:
        con.close()


def get_inbounds(token: str, node: str) -> list[dict]:
    res = subprocess.run(
        ["curl", "-s", "-H", f"Authorization: Bearer {token}", f"{API}/inbounds/{node}"],
        capture_output=True, text=True,
    )
    return json.loads(res.stdout or "[]")


def put_inbound(token: str, node: str, tag: str, body: dict) -> str:
    res = subprocess.run(
        [
            "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-X", "PUT",
            "-H", f"Authorization: Bearer {token}", "-H", "Content-Type: application/json",
            "-d", json.dumps(body), f"{API}/inbounds/{node}/{tag}",
        ],
        capture_output=True, text=True,
    )
    return res.stdout.strip()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--apply", action="store_true", help="perform the PUTs (default: dry-run)")
    args = ap.parse_args()

    token = admin_token()
    old = old_remarks()
    changed = 0

    for node, oldid in NODES.items():
        for ib in get_inbounds(token, node):
            tag = ib.get("tag", "")
            m = TAG_RE.match(tag)
            if not m:
                print(f"{node}/{tag}: unparseable tag, skipped")
                continue
            oid, oport = int(m.group(1)), int(m.group(2))
            remark = old.get((oid, oport))
            cur = ib.get("remark", "")
            if remark is None:
                print(f"{node}/{tag}: no old remark (node {oid} port {oport}); cur={cur!r}")
                continue
            if remark == cur:
                print(f"{node}/{tag}: ok {remark!r}")
                continue
            print(f"{node}/{tag}: {cur!r} -> {remark!r}")
            if not args.apply:
                continue
            body = {
                "tag": tag,
                "port": ib["port"],
                "network": ib["network"],
                "path": ib["path"],
                "host": ib.get("host", ""),
                "remark": remark,
                "route": ib.get("route", "direct"),
                "vnet": ib.get("vnet", False),
            }
            code = put_inbound(token, node, tag, body)
            print(f"    PUT -> {code}")
            if code == "200":
                changed += 1

    if args.apply:
        print(f"changed: {changed}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
