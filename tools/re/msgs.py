import re, struct, sys, json
sys.path.insert(0, '.')
import nwre as n

r = n.SECTIONS['.rdata']
rd = n.data[r[2]:r[2] + r[1]]
names = {}
for m in re.finditer(rb'\0(Javelin\.RPC\.[A-Za-z0-9_.]+)\0', rd):
    names[m.start(1) + r[0]] = m.group(1).decode()

refs = n.code_refs_many(names.keys())
tn = {}  # typename fn -> name
for srva, lst in refs.items():
    for a, t in lst:
        f = n.func_of(a)
        if f and f[1] - f[0] <= 200:
            tn[f[0]] = names[srva]
vts = n.ptrs_many(tn.keys())
msgs = {}
for fn, name in tn.items():
    for vt in vts.get(fn, []):
        slot = lambda k: struct.unpack('<Q', n.read(vt + 8 * k, 8))[0] - n.BASE
        msgs[name] = {'typename_fn': fn, 'vtable_slot0': vt, 'parse': slot(9), 'serialize': slot(10), 'bytesize': slot(7)}
# string field names: which function references each, for later
fieldrefs = {names[s]: sorted({n.func_of(a)[0] for a, t in lst if n.func_of(a)}) for s, lst in refs.items()}
json.dump({'msgs': msgs, 'fieldrefs': fieldrefs}, open('msgs.json', 'w'), indent=1)
print(len(names), 'Javelin.RPC strings,', len(msgs), 'messages with vtables')
for k in sorted(msgs):
    v = msgs[k]
    print(f"{k:50s} parse {v['parse']+n.BASE:x} ser {v['serialize']+n.BASE:x}")
