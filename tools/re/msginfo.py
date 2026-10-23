"""A Hub message type's UUID, type index and unmarshal function, from its
class name: python3 msginfo.py ReceivePlayerSpawnPointMsg [Trait]

Path: the RTTI name of InstallRegistrationHook<Msg>'s lambda -> its
std::function vtable (_Target_type returns the type descriptor; _Do_call is
two slots before) -> the type getter _Do_call calls (it parses the UUID
string and installs the type's factory) -> the factory's slot 5, unmarshal."""
import json
import os
import re
import struct
import sys

import nwre

UUID = re.compile(r"^\{?[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}?$")
here = os.path.dirname(__file__) or "."
ti_path = os.environ.get("NEWWORLD_TYPEINDEX", os.path.join(os.path.dirname(os.path.dirname(nwre.EXE)), "typeindex.json"))
try:
    typeindex = json.load(open(ti_path))["typeIndex"]
except OSError:
    typeindex = []


def q(a):
    return struct.unpack("<Q", nwre.read(a, 8))[0]


def calls_in(fn):
    f = nwre.func_of(fn)
    end = f[1] if f else fn + 0x100
    return [int(o, 16) - nwre.BASE for a, m, o, sz, t in nwre.disasm_at(fn, end=end) if m in ("call", "jmp") and o.startswith("0x")]


def type_getter(fn):
    """(uuid, factory vtable) if fn parses a UUID string."""
    f = nwre.func_of(fn)
    if not f or f[1] - f[0] > 0x400:
        return None
    uuid, vt = None, None
    ins = nwre.disasm_at(f[0], end=f[1])
    for i, (a, m, o, sz, t) in enumerate(ins):
        if t is None:
            continue
        s = nwre.cstr(t, 60)
        if UUID.match(s):
            uuid = s
        elif m == "lea" and o.startswith("rcx") and i + 1 < len(ins) and ins[i + 1][1] == "mov" and ins[i + 1][2] == "qword ptr [rax], rcx":
            vt = t
    return (uuid, vt) if uuid else None


name = sys.argv[1].encode()
trait = sys.argv[2].encode() if len(sys.argv) > 2 else b""
pat = rb"\.\?AV<lambda_\d+>@\?\d\?\?\?\$InstallRegistrationHook@V" + re.escape(name) + rb"@" + (re.escape(trait) + rb"@" if trait else b"") + rb"[ -~]*?\0"
for m in re.finditer(pat, nwre.data):
    td = nwre.off2rva(m.start()) - 0x10
    print(m.group()[:-1].decode())
    for ref, _ in nwre.code_refs(td):
        if nwre.read(ref + 7, 1) != b"\xc3":  # lea rax, [td]; ret
            continue
        for slot in nwre.ptrs_to(ref):
            do_call = q(slot - 8) - nwre.BASE
            for c in calls_in(do_call):
                g = type_getter(c)
                if not g:
                    continue
                u = g[0].strip("{}").replace("-", "").upper()
                idx = typeindex.index(u) if u in typeindex else None
                line = f"  uuid {g[0]}  type index {idx}  getter {c + nwre.BASE:x}"
                if g[1] is not None:
                    line += f"  factory vtable {g[1] + nwre.BASE:x}  unmarshal {q(g[1] + 0x28):x}"
                print(line)
