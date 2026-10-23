"""Message types a dispatcher function compares against: python3 handled.py 0x146b6f190
A type getter is a called function that parses a UUID string; its name is the
string it loads first, if any."""
import json
import os
import re
import sys

import nwre

UUID = re.compile(r"^\{?[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}?$")
here = os.path.dirname(__file__) or "."
index = {}
try:
    for o in json.load(open(os.path.join(here, "typenames.json"))):
        index[o["uuid"]] = (o["index"], o["name"])
except OSError:
    pass


def getter_info(fn):
    f = nwre.func_of(fn)
    if not f or f[1] - f[0] > 0x400:
        return None
    strs = []
    for a, m, o, sz, t in nwre.disasm_at(f[0], end=f[1]):
        if t is not None:
            s = nwre.cstr(t, 80)
            if len(s) >= 3 and s.isprintable():
                strs.append(s)
    uu = [s for s in strs if UUID.match(s)]
    if not uu:
        return None
    names = [s for s in strs if not UUID.match(s)]
    return uu[0], (names[0] if names else "")


f = nwre.func_of(int(sys.argv[1], 16) - nwre.BASE)
seen = set()
for a, m, o, sz, t in nwre.disasm_at(f[0], end=f[1]):
    if m == "call" and o.startswith("0x"):
        fn = int(o, 16) - nwre.BASE
        if fn in seen:
            continue
        seen.add(fn)
        info = getter_info(fn)
        if info:
            u = info[0].strip("{}").replace("-", "").upper()
            idx, nm = index.get(u, (None, ""))
            print(f"{a + nwre.BASE:x} getter {fn + nwre.BASE:x}  {info[0]}  index {idx}  {info[1] or nm}")
