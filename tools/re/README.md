# Reading the client

Python helpers used to recover the gateway protocol from `NewWorld.exe` (build `1.400.6031.6004151`). The exe is not in this repository; point `NEWWORLD_EXE` at your own copy.

```
pip install capstone numpy
export NEWWORLD_EXE=/path/to/NewWorld.exe
python3 msgs.py        # every Javelin.RPC message -> its parser and serializer (msgs.json)
python3 schema.py      # message layouts, from the serializers (docs/schema-1.400.6031.txt)
python3 schema.py Javelin.RPC.LoginToken
python3 offsets.py Javelin.RPC.WorldPublicMetadata   # field -> member offset
```

How it works: the gateway messages are protobuf-lite, so the exe carries no schemas, but every message still has a `GetTypeName()` returning its full name. That leads to the message's vtable (slot 0 = GetTypeName, 9 = _InternalParse, 10 = _InternalSerialize). A serializer writes its fields in field-number order: `mov ecx, <field>` + a WireFormatLite helper (int32, uint32, bool, ...), `stream->WriteStringMaybeAliased(edx = field, ...)` after a UTF-8 check that names the field, or an inline tag byte + the submessage's serializer.
