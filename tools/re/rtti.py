"""Vtables of a class by RTTI name substring: python3 rtti.py <substring>
Prints each match's type descriptor, complete object locator and vtable
with its first slots."""
import re
import struct
import sys

import nwre

pat = sys.argv[1].encode()
D = nwre.SECTIONS[".data"]
R = nwre.SECTIONS[".rdata"]
for m in re.finditer(rb"\.\?AV[ -~]*" + re.escape(pat) + rb"[ -~]*?\0", nwre.data):
    name_rva = nwre.off2rva(m.start())
    td = name_rva - 0x10
    print("type", m.group()[:-1].decode(), "TD", hex(td + nwre.BASE))
    # COL: sig(4) off(4) cdOff(4) pTD(4) pCHD(4) pSelf(4)
    for col in nwre.find_bytes(struct.pack("<I", td), ".rdata"):
        col -= 12
        if struct.unpack("<I", nwre.read(col + 20, 4))[0] != col:
            continue
        for vt in nwre.ptrs_to(col):
            vt += 8
            slots = [struct.unpack("<Q", nwre.read(vt + 8 * i, 8))[0] for i in range(int(sys.argv[2]) if len(sys.argv) > 2 else 6)]
            print("  vtable", hex(vt + nwre.BASE), [hex(s) for s in slots])
