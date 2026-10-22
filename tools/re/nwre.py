"""Helpers for reading NewWorld.exe: sections, strings, code references and
functions (from .pdata), with capstone for disassembly."""
import bisect
import struct

import capstone
import numpy as np

EXE = __import__("os").environ.get("NEWWORLD_EXE", "NewWorld.exe")  # path to the 1.400.6031.6004151 client
BASE = 0x140000000
# name: (rva, vsize, raw offset, raw size)
SECTIONS = {
    ".text": (0x1000, 0x7EC70BD, 0x600, 0x7EC7200),
    ".rdata": (0x7EC9000, 0x1FB151E, 0x7EC7800, 0x1FB1600),
    ".data": (0x9E7B000, 0x9B4498, 0x9E78E00, 0x467000),
    ".pdata": (0xA830000, 0x4D8F44, 0xA2DFE00, 0x4D9000),
}

data = open(EXE, "rb").read()
arr = np.frombuffer(data, dtype=np.uint8)


def off2rva(off):
    for rva, vs, raw, rs in SECTIONS.values():
        if raw <= off < raw + rs:
            return off - raw + rva
    return None


def rva2off(rva):
    for r, vs, raw, rs in SECTIONS.values():
        if r <= rva < r + rs:
            return rva - r + raw
    return None


def read(rva, n):
    o = rva2off(rva)
    if o is None:
        return b""
    return data[o:o + n]


def cstr(rva, maxlen=512):
    b = read(rva, maxlen)
    return b.split(b"\0", 1)[0].decode("latin-1")


def find_bytes(needle, section=".rdata"):
    """All RVAs where needle occurs in a section."""
    rva, vs, raw, rs = SECTIONS[section]
    out, i = [], raw
    while True:
        i = data.find(needle, i, raw + rs)
        if i < 0:
            return out
        out.append(i - raw + rva)
        i += 1


def find_cstring(s):
    """RVAs of a NUL-terminated string (preceded by NUL too)."""
    b = s.encode()
    return [r for r in find_bytes(b + b"\0") if read(r - 1, 1) == b"\0"]


# ---- functions from .pdata ---------------------------------------------------
_p = SECTIONS[".pdata"]
_pd = np.frombuffer(data[_p[2]:_p[2] + _p[1] // 12 * 12], dtype="<u4").reshape(-1, 3)
FUNC_START = _pd[:, 0].copy()
FUNC_END = _pd[:, 1].copy()
_order = np.argsort(FUNC_START)
FUNC_START, FUNC_END = FUNC_START[_order], FUNC_END[_order]


def func_of(rva):
    """(start, end) of the .pdata function holding rva (chained entries are separate)."""
    i = bisect.bisect_right(FUNC_START, rva) - 1
    if i >= 0 and FUNC_START[i] <= rva < FUNC_END[i]:
        return int(FUNC_START[i]), int(FUNC_END[i])
    return None


# ---- rip-relative references ---------------------------------------------------
_t = SECTIONS[".text"]
_text = arr[_t[2]:_t[2] + _t[1]]
_disp = None


def _disps():
    global _disp
    if _disp is None:
        n = len(_text) - 4
        d = (_text[:n].astype(np.int64) | (_text[1:n + 1].astype(np.int64) << 8)
             | (_text[2:n + 2].astype(np.int64) << 16) | (_text[3:n + 3].astype(np.int64) << 24))
        d = np.where(d >= 2**31, d - 2**32, d)
        _disp = d
    return _disp


def refs_to(target_rva, insn_tail=0):
    """Code RVAs of instructions with a rip-relative disp32 pointing at target.
    Tries the common case where the disp32 is the last 4 bytes of the
    instruction (insn_tail=0) and, with insn_tail=k, k immediate bytes after it.
    Returns the RVA of the disp32 field."""
    d = _disps()
    # disp field at text index i: next instruction at i+4+insn_tail
    idx = np.arange(len(d), dtype=np.int64)
    hits = np.nonzero(d + idx + _t[0] + 4 + insn_tail == target_rva)[0]
    return [int(h) + _t[0] for h in hits]


def rip_target(ins):
    """Target RVA of an instruction's rip-relative memory operand, or None."""
    for op in ins.operands:
        if op.type == capstone.x86.X86_OP_MEM and op.mem.base == capstone.x86.X86_REG_RIP:
            return ins.address + ins.size + op.mem.disp - BASE
    return None


def code_refs(target_rva):
    """Instructions referencing target through rip-relative addressing: [(rva, text)]."""
    out = set()
    for tail in (0, 1, 2, 4):
        for drva in refs_to(target_rva, tail):
            for back in range(1, 8):
                start = drva - back
                for ins in md.disasm(read(start, 16), BASE + start):
                    if ins.size == back + 4 + tail and rip_target(ins) == target_rva:
                        out.add((start, ins.mnemonic + " " + ins.op_str))
                    break
    return sorted(out)


# ---- absolute pointers (vtables) ------------------------------------------------
def ptrs_to(target_rva, section=".rdata"):
    return find_bytes(struct.pack("<Q", BASE + target_rva), section)


# ---- disassembly ---------------------------------------------------------------
md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
md.detail = True


def disasm_at(rva, count=None, end=None):
    """[(rva, mnemonic, op_str, size, rip_target)] starting at rva."""
    size = (end - rva) if end else 64 * (count or 1)
    out = []
    for ins in md.disasm(read(rva, size), BASE + rva):
        out.append((ins.address - BASE, ins.mnemonic, ins.op_str, ins.size, rip_target(ins)))
        if count and len(out) >= count:
            break
    return out


def disasm_func(rva):
    f = func_of(rva)
    if not f:
        return []
    return disasm_at(f[0], end=f[1])


def describe(t):
    """A short note for a referenced RVA: the string there, if any."""
    if t is None:
        return ""
    r = SECTIONS[".rdata"]
    if r[0] <= t < r[0] + r[1]:
        s = cstr(t, 100)
        if len(s) >= 3 and all(32 <= ord(c) < 127 for c in s):
            return f'   ; "{s}"'
    return f"   ; -> {t + BASE:x}"


def show(rva, end=None, count=None):
    return "\n".join(f"{a + BASE:x}: {m} {o}{describe(t)}" for a, m, o, sz, t in disasm_at(rva, count=count, end=end))


def code_refs_many(targets, tails=(0, 1, 2, 4), chunk=1 << 24):
    """code_refs for many targets in one pass: {target: [(rva, text)]}."""
    targets = np.array(sorted(set(targets)), dtype=np.int64)
    d = _disps()
    cands = []
    for tail in tails:
        for s in range(0, len(d), chunk):
            seg = d[s:s + chunk]
            t = seg + np.arange(s, s + len(seg), dtype=np.int64) + (_t[0] + 4 + tail)
            hit = np.nonzero(np.isin(t, targets))[0]
            cands += [(int(h) + s + _t[0], tail, int(t[h])) for h in hit]
    out = {}
    for drva, tail, target in cands:
        for back in range(1, 8):
            start = drva - back
            for ins in md.disasm(read(start, 16), BASE + start):
                if ins.size == back + 4 + tail and rip_target(ins) == target:
                    out.setdefault(target, set()).add((start, ins.mnemonic + " " + ins.op_str))
                break
    return {k: sorted(v) for k, v in out.items()}


def ptrs_many(targets, section=".rdata"):
    """{target: [rva of an 8-byte absolute pointer to it]} in one pass."""
    r = SECTIONS[section]
    q = np.frombuffer(data[r[2]:r[2] + (r[3] // 8) * 8], dtype="<u8")
    want = np.array(sorted(BASE + t for t in set(targets)), dtype=np.uint64)
    out = {}
    for align in range(0, 8, 8):  # vtables are 8-aligned
        hits = np.nonzero(np.isin(q, want))[0]
        for h in hits:
            out.setdefault(int(q[h]) - BASE, []).append(int(h) * 8 + r[0])
    return out


def disasm_until_ret(rva, maxn=200):
    """Instructions from rva up to the first ret (or a tail jmp to another function)."""
    out = []
    for ins in disasm_at(rva, count=maxn):
        out.append(ins)
        if ins[1] == "ret" or (ins[1] == "jmp" and ins[2].startswith("0x") and not (rva <= int(ins[2], 16) - BASE < rva + 0x400)):
            break
    return out


def rtti_vtables(substr, section=".data"):
    """MSVC RTTI: TypeDescriptor names containing substr -> their vtables.
    TypeDescriptor = {vftable ptr, spare, name}; CompleteObjectLocator (in
    .rdata) = {sig=1, off, cdoff, td rva, chd rva, self rva}; vtable[-1] = &COL."""
    import re as _re
    r = SECTIONS[section]
    sec = data[r[2]:r[2] + r[3]]
    out = []
    for m in _re.finditer(_re.escape(substr.encode()), sec):
        start = sec.rfind(b"\0", 0, m.start()) + 1
        name = sec[start:sec.find(b"\0", m.start())].decode("latin-1")
        td = start + r[0] - 0x10
        col_pat = struct.pack("<I", td)
        for hit in find_bytes(col_pat, ".rdata"):
            col = hit - 12
            sig = struct.unpack("<I", read(col, 4))[0]
            selfrva = struct.unpack("<I", read(col + 20, 4))[0]
            if sig != 1 or selfrva != col:
                continue
            for p in ptrs_to(col):
                out.append((name, p + 8))
    return out


def func_impl_vtables(td_name_rva):
    """std::function storage for a lambda: its _Target_type() is
    `lea rax, [TypeDescriptor]; ret`, found through code refs; the vtable
    holding it gives _Do_call at slot 2 (slot order: _Copy, _Move, _Do_call,
    _Target_type, _Delete_this, _Get)."""
    td = td_name_rva - 0x10
    out = []
    for a, t in code_refs(td):
        f = func_of(a) or (a, a + 8)
        for p in ptrs_to(f[0]) + ptrs_to(a):
            out.append((a, p))
    return out


def func_strings(rva, maxlen=100):
    """Strings referenced by the function containing rva."""
    f = func_of(rva)
    ins = disasm_at(f[0], end=f[1]) if f else disasm_until_ret(rva)
    out = []
    for a, mn, o, sz, t in ins:
        d = describe(t)
        if d.startswith('   ; "'):
            out.append(d[6:-1][:maxlen])
    return out


def func_calls(rva):
    f = func_of(rva)
    ins = disasm_at(f[0], end=f[1]) if f else disasm_until_ret(rva)
    return [int(o, 16) - BASE for a, mn, o, sz, t in ins if mn == "call" and o.startswith("0x")]


def call_refs(target_rva):
    """Direct `call rel32` / `jmp rel32` sites targeting target_rva."""
    out = []
    for drva in refs_to(target_rva, 0):
        op = read(drva - 1, 1)
        if op in (b"\xe8", b"\xe9"):
            out.append(drva - 1)
    return out
