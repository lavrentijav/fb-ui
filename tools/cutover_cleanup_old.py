#!/usr/bin/env python3
"""Remove ru3-ru6 and Poland from the old central panel via its own API.

Dry-run by default. Aborts if any client would be left without a surviving
inbound. The API token is read from the bot's data/.env and never printed.
"""

from __future__ import annotations

import json
import sqlite3
import ssl
import sys
import urllib.request
from pathlib import Path

ENV = Path("/opt/appuser/firewalkbot-dev/data/.env")
DB = "/etc/x-ui/x-ui.db"
NODE_IDS = [4, 5, 10, 11]          # ru4, ru6, ru3, ru5
POLAND_INBOUND = 26                 # local "Польша Варшава" (disabled)


def config() -> dict[str, str]:
    out: dict[str, str] = {}
    for line in ENV.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        out[key.strip()] = value.strip().strip('"').strip("'")
    return out


def request(base: str, token: str, method: str, path: str):
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    req = urllib.request.Request(
        base + path,
        method=method,
        headers={"Authorization": "Bearer " + token, "Accept": "application/json"},
    )
    with urllib.request.urlopen(req, context=ctx, timeout=30) as resp:
        return resp.status, json.loads(resp.read().decode())


def risk_counts() -> tuple[int, int]:
    con = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
    try:
        only_ru = con.execute(
            """
            select count(*) from clients c
            where exists (select 1 from client_inbounds ci join inbounds i on i.id=ci.inbound_id
                          where ci.client_id=c.id and i.node_id in (4,5,10,11))
              and not exists (select 1 from client_inbounds ci join inbounds i on i.id=ci.inbound_id
                          where ci.client_id=c.id and (i.node_id not in (4,5,10,11) or i.node_id is null))
            """
        ).fetchone()[0]
        only_pl = con.execute(
            """
            select count(*) from clients c
            where exists (select 1 from client_inbounds ci where ci.client_id=c.id and ci.inbound_id=?)
              and not exists (select 1 from client_inbounds ci where ci.client_id=c.id and ci.inbound_id<>?)
            """,
            (POLAND_INBOUND, POLAND_INBOUND),
        ).fetchone()[0]
        return int(only_ru), int(only_pl)
    finally:
        con.close()


def main() -> int:
    apply = "--apply" in sys.argv
    cfg = config()
    base = cfg["XUI_API_URL"].rstrip("/")
    token = cfg["XUI_API_TOKEN"]

    only_ru, only_pl = risk_counts()
    print(f"clients with no surviving inbound: ru3-6={only_ru} poland={only_pl}")
    if only_ru or only_pl:
        print("refusing: deletion would orphan clients", file=sys.stderr)
        return 2

    _, inbounds = request(base, token, "GET", "/panel/api/inbounds/list")
    _, nodes = request(base, token, "GET", "/panel/api/nodes/list")
    rows = inbounds.get("obj") or []
    node_rows = nodes.get("obj") or []

    targets = [
        row for row in rows
        if row.get("nodeId") in NODE_IDS or row.get("id") == POLAND_INBOUND
    ]
    print(f"panel: {len(rows)} inbounds, {len(node_rows)} nodes")
    for row in sorted(targets, key=lambda r: r["id"]):
        print(f"  delete inbound id={row['id']} node={row.get('nodeId')} enable={row.get('enable')} remark={row.get('remark')}")
    wanted = {row["id"] for row in targets}
    expected = {26, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 62, 63, 64, 65, 66, 67, 68, 69}
    if not apply:
        missing = expected - wanted
        extra = wanted - expected
        print(f"dry-run: would delete {len(wanted)} inbounds, {len(NODE_IDS)} nodes; missing={sorted(missing)} extra={sorted(extra)}")
        return 0
    if wanted != expected:
        print(f"refusing: live inbound set differs from the reviewed plan (missing={sorted(expected-wanted)}, extra={sorted(wanted-expected)})", file=sys.stderr)
        return 3

    for inbound_id in sorted(wanted):
        status, body = request(base, token, "POST", f"/panel/api/inbounds/del/{inbound_id}")
        print(f"inbound {inbound_id}: HTTP {status} success={body.get('success')} msg={body.get('msg')}")

    for node_id in NODE_IDS:
        status, body = request(base, token, "POST", f"/panel/api/nodes/del/{node_id}")
        print(f"node {node_id}: HTTP {status} success={body.get('success')} msg={body.get('msg')}")

    _, inbounds2 = request(base, token, "GET", "/panel/api/inbounds/list")
    _, nodes2 = request(base, token, "GET", "/panel/api/nodes/list")
    print(f"after: {len(inbounds2.get('obj') or [])} inbounds, {len(nodes2.get('obj') or [])} nodes")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
