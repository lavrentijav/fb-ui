#!/usr/bin/env python3
"""Add fi2 as a second master on a node so the picker chooses the best one."""
import json
import sys

p = sys.argv[1] if len(sys.argv) > 1 else "/etc/fbnode/config.json"
c = json.load(open(p))
c.setdefault("masters", {})["fi2"] = "tls://fi2.firewalk.top:1443"
c["rebindEvery"] = "10m"
json.dump(c, open(p, "w"), indent=2)
print("masters:", c["masters"], "rebindEvery:", c.get("rebindEvery"))
