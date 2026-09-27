#!/usr/bin/env python3
"""Call this host's own panel API as root, without knowing its port or path.

usage: panelapi.py GET edge/status
       panelapi.py POST edge/routes/save '{"kind":"sni","match":"a.example.com","action":"panel","enable":true}'
"""
import json
import os
import re
import ssl
import subprocess
import sys
import urllib.request


def sh(cmd):
    return subprocess.run(cmd, shell=True, check=True, capture_output=True, text=True).stdout


def token():
    f = "/root/.fbui-api"
    if not os.path.exists(f) or os.path.getsize(f) == 0:
        tok = re.search(r"apiToken: (\S+)", sh("/usr/local/x-ui/x-ui setting -getApiToken true 2>/dev/null")).group(1)
        old = os.umask(0o077)
        open(f, "w").write(tok)
        os.umask(old)
    return open(f).read().strip()


def call(method, ep, body=None):
    show = sh("/usr/local/x-ui/x-ui setting -show true 2>/dev/null")
    port = re.search(r"^port: *(\d+)", show, re.M).group(1)
    base = re.search(r"webBasePath: *(\S+)", show).group(1).rstrip("/")
    data = body.encode() if body else None
    req = urllib.request.Request(f"https://127.0.0.1:{port}{base}/panel/api/{ep}", data=data, method=method,
                                 headers={"Authorization": "Bearer " + token(), "Content-Type": "application/json"})
    ctx = ssl._create_unverified_context()  # our own panel over loopback
    return json.load(urllib.request.urlopen(req, timeout=60, context=ctx))


if __name__ == "__main__":
    print(json.dumps(call(sys.argv[1], sys.argv[2], sys.argv[3] if len(sys.argv) > 3 else None), ensure_ascii=False))
