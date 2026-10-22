"""Recover message layouts from the protobuf-lite _InternalSerialize functions.

A serializer writes its fields in field-number order. Each field shows up as
either `mov ecx, <field>` + `call <WireFormatLite helper>`, or an inline tag
byte (`mov byte ptr [reg], <tag>`) followed by an inline varint and a call to
a submessage's own serializer. String fields are preceded by a UTF-8 check that
names the field ("Javelin.RPC.Msg.field")."""
import json
import re
import sys
from collections import defaultdict

sys.path.insert(0, ".")
import nwre as n

M = json.load(open("msgs.json"))["msgs"]
SER = {v["serialize"]: k for k, v in M.items()}

# WireFormatLite helpers, classified by reading their bodies (see notes).
KNOWN = {
    0x6099E60: "int32",   # sign-extends the value: int32 or enum
    0x609E1F0: "uint32",  # zero-extends
    0x609E170: "bool",    # writes the value's low byte
}
# stream->WriteStringMaybeAliased(field = edx, value = r8, target = r9)
STRING_WRITERS = {0x6099F10}


def helper_kind(h, cache={}):
    if h in cache:
        return cache[h]
    if h in KNOWN:
        cache[h] = KNOWN[h]
        return KNOWN[h]
    kind = None
    body = n.disasm_until_ret(h, 300)
    # per-type InternalWriteMessage<T>: tail-jumps to T's serializer
    for a, mn, o, sz, t in body:
        if mn in ("jmp", "call") and o.startswith("0x") and int(o, 16) - n.BASE in SER:
            kind = "msg:" + SER[int(o, 16) - n.BASE]
            break
    else:
        # a submessage writer with T's serializer inlined: it writes a length
        # (GetCachedSize) and then fields of its own through known helpers
        inner = [int(o, 16) - n.BASE for a, mn, o, sz, t in body if mn == "call" and o.startswith("0x")]
        if any(i in KNOWN or i in STRING_WRITERS for i in inner):
            kind = f"msg:?inlined@{h + n.BASE:x}"
    cache[h] = kind
    return kind


def analyze(name):
    f = n.func_of(M[name]["serialize"])
    if not f:
        return None, ["no pdata for serializer"]
    ins = n.disasm_at(f[0], end=f[1])
    back = [(a, int(o, 16) - n.BASE) for a, mn, o, sz, t in ins
            if mn.startswith("j") and o.startswith("0x") and int(o, 16) - n.BASE < a]
    fields = []  # (addr, number, kind, name, note)
    last_ecx = None
    last_edx = None
    last_name = None
    pending_tag = None
    unknown = []
    for a, mn, o, sz, t in ins:
        if mn == "mov" and re.fullmatch(r"ecx, (0x[0-9a-f]+|\d+)", o):
            last_ecx = int(o.split(", ")[1], 0)
        if mn == "mov" and re.fullmatch(r"edx, (0x[0-9a-f]+|\d+)", o):
            last_edx = int(o.split(", ")[1], 0)
        if mn == "lea" and t is not None:
            s = n.cstr(t, 120)
            if s.startswith("Javelin.RPC."):
                last_name = s
        m = re.fullmatch(r"byte ptr \[r[a-z0-9]+\], (0x[0-9a-f]+|\d+)", o) if mn == "mov" else None
        if m:
            tag = int(m.group(1), 0)
            if 8 <= tag < 0x80 and (tag & 7) in (0, 1, 2, 5):
                pending_tag = (a, tag)
        if mn in ("call", "jmp") and o.startswith("0x"):
            tgt = int(o, 16) - n.BASE
            if tgt in SER and pending_tag and a - pending_tag[0] < 0x80:
                fields.append((a, pending_tag[1] >> 3, "msg:" + SER[tgt], None, "inline"))
                pending_tag = None
                continue
            nm = last_name.rsplit(".", 1)[1] if last_name and last_name.rsplit(".", 1)[0] == name else None
            if tgt in STRING_WRITERS and last_edx is not None:
                fields.append((a, last_edx, "string" if nm else "bytes", nm, ""))
                last_edx = None
                last_name = None
                continue
            if last_ecx is not None and mn == "call":
                k = helper_kind(tgt)
                if k is None:
                    k = f"helper_{tgt + n.BASE:x}"
                    unknown.append((last_ecx, k))
                fields.append((a, last_ecx, k, None, ""))
                last_ecx = None
                last_name = None
    out = []
    prev = f[0]
    for a, num, kind, nm, note in fields:
        rep = any(src > a and prev < dst <= a for src, dst in back)
        out.append({"field": num, "kind": kind, "name": nm, "repeated": rep})
        prev = a
    return out, unknown


if __name__ == "__main__":
    want = sys.argv[1:] or sorted(M)
    allunknown = defaultdict(list)
    for name in want:
        fs, unk = analyze(name)
        for num, k in unk:
            allunknown[k].append(f"{name}.{num}")
        print(f"message {name} {{")
        for fd in fs or []:
            rep = "repeated " if fd["repeated"] else ""
            print(f"  {rep}{fd['kind']} {fd['name'] or 'f' + str(fd['field'])} = {fd['field']};")
        print("}")
    if allunknown:
        print("\n# unclassified helpers:")
        for k, v in sorted(allunknown.items(), key=lambda kv: -len(kv[1])):
            print(k, len(v), v[:6])
