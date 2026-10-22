"""Field -> member offset, from a serializer: the last `[rdi + OFF]` read before each field is written."""
import re, sys
sys.path.insert(0, '.')
import nwre as n
import schema

def offsets(name):
    f = n.func_of(schema.M[name]['serialize'])
    ins = n.disasm_at(f[0], end=f[1])
    fields, _ = schema.analyze(name)
    out, last_off, idx = {}, None, 0
    # walk again, pairing each field-writing call with the last member read
    calls = []
    for a, mn, o, sz, t in ins:
        m = re.search(r'\[r(?:di|si|bx|cx|bp|14|15) \+ (0x[0-9a-f]+)\]', o)
        if m and mn in ('mov', 'movzx', 'movsxd', 'lea', 'cmp', 'movss', 'movsd') and int(m.group(1), 16) >= 0x10:
            last_off = int(m.group(1), 16)
        if mn in ('call', 'jmp') and o.startswith('0x'):
            calls.append((a, last_off))
    return fields, calls

if __name__ == '__main__':
    name = sys.argv[1]
    f = n.func_of(schema.M[name]['serialize'])
    for l in n.show(f[0], end=f[1]).split('\n'):
        if re.search(r'\[rdi \+ 0x[0-9a-f]+\]|mov ecx, |mov edx, 0x|mov edx, \d|Javelin\.RPC\.|byte ptr \[rbx\], 0x', l):
            print(l)
