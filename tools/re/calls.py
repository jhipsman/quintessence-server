"""Direct callers (call/jmp rel32) of a function: python3 calls.py 0x145a87010"""
import sys

import numpy as np

import nwre

target = int(sys.argv[1], 16) - nwre.BASE
d = nwre._disps()
idx = np.arange(len(d), dtype=np.int64)
hits = np.nonzero(d + idx + nwre._t[0] + 4 == target)[0]
for h in hits:
    rva = int(h) + nwre._t[0] - 1
    op = nwre.read(rva, 1)
    if op in (b"\xe8", b"\xe9"):
        f = nwre.func_of(rva)
        print(hex(rva + nwre.BASE), "call" if op == b"\xe8" else "jmp", "in", f and hex(f[0] + nwre.BASE))
