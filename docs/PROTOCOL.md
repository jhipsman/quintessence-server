# What we know about the protocol

New World build `1.400.6031.6004151`, started by the Quintessence launcher with `-GatewayMode stubbed -GatewayAddr <host:port>`. Everything here comes from captures of the real client and from strings in `NewWorld.exe` (the `NAMES.txt` that `protoextract` writes; run it on your own copy, it isn't kept in this repository). The message layouts in the last part were recovered from the client's code, with field numbers for every field.

## Transport

- The gateway is **gRPC over cleartext HTTP/2** (h2c, prior knowledge) on TCP, client `grpc-c++/1.33.1 grpc-c/13.0.0 (windows; chttp2)`.
- It accepts `identity`, `deflate` and `gzip` message encodings.
- The messages are proto3, built with the protobuf **lite** runtime, so the exe holds no schemas. They come from `Login.pb.cc` in `...\ControlPort\protos\` of the game's source tree.
- In "stubbed" mode the client logs `ConfigureLogin MODE_STUBBED: endpoint=%s, persona=%s`.

## Login flow (from the client's own names)

1. `GetLoginInfo`: the account's worlds and characters (the C++ side turns `GetLoginInfoResult` into a `LoginInfoList`).
2. `ListWorlds` / `GetWorldList`: the world list.
3. `QueueLogin(world_id, character_id)` → `QueueLoginResult{ticket_id}`, then `RefreshQueuedLogin(ticket_id)` until the queue lets the player in (`Waiting in login queue... Ticket (%s), Position (%d)`).
4. The client gets a `LoginToken` and connects to the world server it names (`rep_endpoint`). The world server checks the token (`@mm_authresult_Denied_LoginToken*`: expired, signature, world id, wrong host, endpoint, capabilities).

## Services

`Javelin.RPC.StubbedGatewayService`: `GetLoginInfo`, `GetWorldList`, `ListWorlds`, `GetCharacterList`, `GetCharactersPayload`, `GetGameCharacterNames`, `CreateCharacter`, `ValidateCharacter`, `PatchCharacter`, `DeactivateCharacter`, `QueueLogin`, `RefreshQueuedLogin`, `LinkIdentity`, `UnlinkIdentity`.

Also stubbed: `StubbedAchievementService`, `StubbedAgsPrivacySettingsService`, `StubbedFcsService` (store and entitlements), `StubbedLeaderboardService`, `StubbedMatchmakingService`, `StubbedNotificationService/ReceiveMessages`. Server-side only: `Amazon.Hub.RPC.*`, `Manifold.REP.REPControl/*`.

## Messages

Captured on the wire:

```
GetLoginInfoRequest {
  1: PersonaId { 1: id = "<player name>" }   // persona from the launcher's patch #4
  2: "dev"                                    // channel_id? (the launcher passes -ProductId dev)
  3: 1                                        // not a string: a bool or a number
}
```

String fields known by name (numbers unknown unless noted):

| Message | String fields |
|---|---|
| `GetLoginInfoRequest` | `channel_id` |
| `PersonaId` | `id` (field 1, captured) |
| `GetLoginInfoResult` | none. **Field 3: repeated character** (capture 4). |
| character in `GetLoginInfoResult` field 3 | **field 4: name** (capture 4); probably `CharacterMetadata` |
| `WorldPublicMetadata` | `id`, `name`, `publicName`, `status`, `version`, `worldSet`, `worldType`, `transferToRegion` |
| `WorldMetrics` | `worldPopulationStatus` |
| `RecommendedWorld` | `recommendationType` |
| `PendingWorldMerge` | `sourceWorldId`, `targetWorldId`, `scheduledDate` |
| `CharacterMetadata` | `character_id`, `persona_id`, `world_id`, `name`, `status`, `modified_date`, `name_modified_date`, `name_latent_date`, `must_rename_reason`, `must_transfer_date`, `transfer_date`, `region_transfer_date`, `published_source`, `published_social_source` |
| `CharacterCreationParameters` | `name` |
| `CreateCharacterRequest` | `world_id` |
| `CreateCharacterResult` | `character_id` |
| `ValidateCharacterRequest` | `name`, `world_id` |
| `QueueLoginRequest` | `world_id`, `character_id`, `steam_user_id` |
| `QueueLoginResult` | `ticket_id` |
| `RefreshQueuedLoginRequest` | `ticket_id` |
| `LoginToken` | `persona_id`, `character_id`, `world_id`, `src_world_id`, `ticket_id`, `match_id`, `location_id`, `locationGroup_id`, `rep_endpoint`, `host_hash`, `signature`, `jwt_claims`, `client_capabilities`, `steam_user_id` |
| `ListWorldsRequest` | `cognitoPoolId`; `Filter.filterType`, `Filter.FilterParams` (map) |

## Client error texts worth knowing

`Errors::login_no_worlds`, `login_world_list_failed`, `login_character_list_failed`, `login_no_ticket`, `login_invalid_ticket`, `login_queue_join_failed`, `login_unable_to_connect`, `login_not_authorized`, `login_bad_request`, `Login gateway is not available`, `Cannot connect to Gateway service`.

## Captures

1. 2026-10-22, stub v1: TCP + HTTP/2 preface; the client gives up after 20 s with no HTTP/2 answer.
2. 2026-10-22, stub v3: `GetLoginInfo` as above; answered `Unimplemented`, the client retries every 2 s.
3. 2026-10-22, stub v4 (`-reply empty`): the client accepts an empty `GetLoginInfoResult` (no error, no retry loop) but makes no further call; it asks `GetLoginInfo` again later (6 s, then 36 s). An empty result most likely reads as "no worlds, no characters".
4. 2026-10-22, stub v5 (`-reply probe`): **character select screen.** One character named `@3@4@_@_`, marked "World Unavailable"; world dropdown "Local Host 1/0"; Play greyed out. So `GetLoginInfoResult` field 3 = repeated characters, character field 4 = name. Probe 1 answers `GetLoginInfo` with a message that fills fields 1-20, each with fields 1-15 holding labels of their position (`probe.go`), to see which fields the game shows as worlds and characters.
5. 2026-10-22, stub v6 (`-reply probe2`, every text `@W@W@W@W` except the character name `@N@a@m@e`): **no character listed** (4 × Create Character), header "Local Host 0/4". The dropdown lists only **"Local Host" (0 ms, 0/0)**: a world the client adds itself in stubbed mode. Reading: the character's world_id now matches the worlds in our reply, which the dropdown filters out, so the character moved there.
6. 2026-10-22, stub v7 (`-reply probe3`): **no character at all**, not even the ones whose world_id stayed a unique label, nor `@E@m@p@t`. So the world-match reading of capture 5 is incomplete: something in the other top-level fields (all `@W@W@W@W` in probes 2 and 3, labels in probe 1) decides whether characters show. Probe 3 was: 14 characters, character j has only field j = `@W@W@W@W` (named `@C@j@_@_`), plus `@E@m@p@t` with every field empty. The one missing from the list has world_id in field j.

## Clues from the exe strings (second NAMES.txt)

- "Local Host" likely comes from an embedded JSON channel config: `{"channelName": "localhost", "localhost": {"displa…` (cut off; the next protoextract keeps JSON whole).
- Worlds can come from a local file: `@assets@/worlds.override.json`, "Local override location of worlds dynamic data"; `CreateDefaultWorld`, `DevWorld`, `@mm_defaultworldname`.
- Worlds are filtered: `MainMenu.IsWorldVersionMatch`; "When enabled, UI will show worlds which are flagged as hidden"; `SelectWorldByType - Invalid match. worldType=%d; best world=%s, type=%d, version=%s`. `Unable to find %s world, will use default/empty world id.`
- Character limits: `MaxAccountCharacters`, `MaxChannelCharacters`, `TooManyCharactersForWorld`.
- `World Metrics: worldAgeDays=%d, queueSize=%d, queueWaitTimeSec=%d, worldPopulationStatus=%s`.

## Recovered from the client's code (2026-10-22)

The message layouts below come from the client's own `_InternalSerialize` functions (`tools/re/`); all 126 `Javelin.RPC` messages are in `schema-1.400.6031.txt`. Names in *italics* are inferred from how the client uses the field.

```
GetLoginInfoRequest   { 1 PersonaId; 2 string channel_id; 3 bool }
GetLoginInfoResult    { 1 int32 *error* (non-zero = the client shows an error);
                        2 repeated WorldPublicMetadata; 3 repeated CharacterMetadata;
                        4 repeated CharacterNameData; 5 uint32 *max account characters*;
                        6 uint32 *max channel characters* (0 = no character slot in the menu);
                        7 repeated PendingWorldMerge }
WorldPublicMetadata   { 1 id; 2 name; 3 status; 4 int32 *publicStatusCode*; 5 version;
                        6 publicName; 7 worldType; 8 int32 *maxAccountCharacters*; 9 worldSet;
                        10 WorldMetrics; 11 transferToRegion; 12 bool *isFull*; 13 bool *isRecommended* }
WorldMetrics          { 1 int32 *worldAgeDays*; 2 int32 *queueSize*; 3 int32 *queueWaitTimeSec*;
                        4 worldPopulationStatus }
CharacterMetadata     { 1 character_id; 2 persona_id; 3 world_id; 4 name;
                        5 PublishedCharacterMetadata; 6 published_source; 7 uint32; 8 CharacterTransferData;
                        9, 10 bool; 11 bytes; 12 published_social_source; 13 uint32; 14 status;
                        15 transfer_date; 16 region_transfer_date; 17, 18 bool; 19 must_rename_reason;
                        20 name_modified_date; 21 modified_date; 22 name_latent_date;
                        23 must_transfer_date; 24, 25 bool }
CreateCharacterRequest  { 1 PersonaId; 2 CharacterCreationParameters; 3 world_id }
CharacterCreationParameters { 1 name; 2 PublishedCharacterMetadata; 3 bytes; 4 bool }
CreateCharacterResult   { 1 int32 *error*; 2 character_id }
ValidateCharacterRequest{ 1 world_id; 2 name }      ValidateCharacterResult { 1 int32 *error* }
QueueLoginRequest     { 1 PersonaId; 2 character_id; 3 uint32; 4 steam_user_id; 5 world_id; 6 bool }
QueueLoginResult      { 1 int32 *error*; 2 ticket_id; 3 QueueStats {1,2,3 int32}; 4 LoginToken }
LoginToken            { 1 rep_endpoint; 2 world_id; 3 character_id; 4 persona_id; 5 uint32;
                        6 steam_user_id; 7 uint32; 8 signature; 9 client_capabilities; 10 ticket_id;
                        11 int32; 12 locationGroup_id; 13 location_id; 14 uint32; 15 bool; 16 host_hash;
                        17 bool; 18 match_id; 19 src_world_id; 20 jwt_claims; 21 bool }
```

What the client checks:

- **GetLoginInfoResult field 1 must be 0**, or the callback reports an error instead of converting the result.
- **World version:** a world matches when its `version` *contains* the build's changelist `6004151` (`1.400.6031.6004151` does), unless `javelin.select-world-allow-version-mismatch` is set (fn `0x1441dcda0`).
- **worldType:** `"FTUE"` → 0, `"OpenWorld"` → 1, anything else → 2 (fn `0x1417c40a0`).
- **Character status:** a character is active when `status == "ACTIVE"` (fn `0x1417423e0`).
- The client's world record, as its UI scripts see it: `worldAgeDays, queueSize, queueWaitTimeSec, worldPopulationStatus, publicStatusCode, publicName, maxAccountCharacters, worldSet, worldMetrics, transferToRegion, isFull, isRecommended`.
- **Login queue** (`0x146457700`, `0x14643d110`): `QueueLoginResult` field 1 non-zero = error; `QueueStats` (field 3) first number non-zero = still queued; otherwise the `LoginToken` (field 4) is taken. The client only acts on a token whose **signature** (field 8) differs from the last one it acted on, starting from an empty one, then calls `StartREPLogin` (`0x146468370`) to connect to `rep_endpoint`. An empty signature reads as a repeat and the client polls `RefreshQueuedLogin` every frame. The stubbed client swaps a `rep_endpoint` host of `0.0.0.0` for the gateway's host (`0x146416f30`).
- `QueueLoginRequest` from the client carries the persona and the character id only (no world id); `RefreshQueuedLoginRequest` only the ticket id.
- Useful console variables: `g_showHiddenWorlds`, `g_loginQueueBypass`, `g_maintenanceModeBypass`, `g_createCharacterAccountLimitBypass`, `g_selectWorldId`, `g_loginWithWorldTypes`.

Probe results, explained: probe 1's character pointed at world `@3@3@_@_` while the world's id was `@2@1@_@_` ("World Unavailable"); in probes 2 and 3 the ids matched but the world's version (`@W@W@W@W`) failed the version check, taking its characters with it.

## Gateway runs (2026-10-22)

1. v8: the world "Quintessence" listed in View Worlds (queue 0, population LOW); character menu 0/0, no slot.
2. v9 (fields 5 and 6 = 4): **character creation works** (ValidateCharacter, CreateCharacter with a 1964-byte request holding the appearance, character listed); Play opens "Logging in… Position in queue 1" and the client calls RefreshQueuedLogin 779 times, every ~25 ms: our token had no signature.
3. v10 (signed token): **the client leaves the queue and connects to the world server ("REP") at the token's `rep_endpoint`**: UDP from local port 27000 (27001, 27002 on later tries) to 127.0.0.1:6000, **DTLS 1.2 handshake completes** with our self-signed certificate (TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384), then application data. Unanswered, the client shows "Connection Error: Unable to connect to the server" after ~18 s.

## REP (world server) protocol: first look

Decrypted client datagrams (capture of 2026-10-22 21:04):

```
80 01 00 02  a0 00 05  03 00 00 ff ff  00 00 00 05 01 88 00 06 ff ff 00 00 00 05 01 01 88 00 02 ff ff  20 06
80 01 00 03  a0 00 07  03 00 03 ff ff  00 00 00 05 01 01 01 88 00 02 ff ff  20 06
80 01 00 07  a0 00 02  03 00 0b ff ff  20 06                                        (every ~115 ms)
80 01 00 11  a0 00 0d  03 00 17 ff ff  00 00 00 05 01 01 01 01 01 01 01 01 01 88 00 02 ff ff  20 06   (every ~1 s)
```

- `80 01` + a 16-bit counter, +1 per datagram: packet sequence.
- `a0 00 XX`: a block of XX bytes (2 for the heartbeats).
- `03 00 YY ff ff`: a second counter (YY grows faster than the packet sequence) and `ff ff`, probably "nothing received yet" (an ack).
- Every ~1 s a larger datagram whose run of `01` grows by one each time: most likely a connection request re-sent, with a retry or reliability counter.
- Every datagram ends `20 06`.

(First reading, before the framing was known. The corrected reading is below.)

## REP transport: GridMate Carrier (2026-10-23)

The decrypted datagrams are Lumberyard's GridMate Carrier (`Carrier.cpp`), with a 2-byte New World prefix in front. Everything is big endian.

```
datagram = prefix (2: 80 01, meaning unknown, the server echoes it) | datagram seq (u16) | messages
message  = flags (u8) | data size (u16)
           [channel (u8)    if MF_DATA_CHANNEL 0x20]
           [chunks (u16)    if MF_CHUNKS 0x04]
           [seq (u16)       unless MF_SEQUENTIAL_ID 0x08: previous + 1]
           [rel seq (u16)   unless MF_SEQUENTIAL_REL_ID 0x10: previous, +1 if reliable]
           data
flags: MF_RELIABLE 0x01, MF_CONNECTING 0x80 (sent before the connection is up), 0x42 unused
```

So `80 01 00 02 | a0 00 05 03 00 00 ff ff 00 00 00 05 01 | 88 00 06 ff ff 00 00 00 05 01 01 | 88 00 02 ff ff 20 06` is datagram 2 with three channel-3 (system) messages: SM_CONNECT_REQUEST (version `00 00 00 05`, id `01`), the same again (the extra `01` is a Lumberyard quirk: each retry appends the id byte once more), and SM_CT_ACKS keep-alive (`20` = AHF_KEEP_ALIVE, id `06`).

System message ids (last data byte): 1 CONNECT_REQUEST, 2 CONNECT_ACK, 3 DISCONNECT, 4 CLOCK_SYNC, 6 CT_ACKS, 7 CT_CONN_CONTROL, 8 CT_BANDWIDTH. Ack data: flags `0x80|n` = last seq + n bitmap bytes; `0x40` = last, first (continuous range); `0x20` keep-alive; otherwise just the last seq.

The server's answer (as `CarrierImpl::ProcessSystemMessages` does it): SM_CLOCK_SYNC (u32 ms, unreliable), then SM_CONNECT_ACK, reliable, carrying the version back, plus acks for the client's datagrams. `carrier.go` implements this, re-sends unacked reliable messages after 250 ms and sends keep-alive acks. Whether New World's handshake is the default one (version only) is the next thing a capture will tell.


## REP messages above the carrier (2026-10-23)

First test of the carrier connect (capture 2026-10-22 21:16): the client accepted SM_CONNECT_ACK and sent its login, then, with no answer, an SM_DISCONNECT (reason 0) after ~15 s and a new try from the next port.

- **Compression.** Bit `0x01` of the first prefix byte (`81 01`) means the datagram after its 4-byte header is one LZ4 block. Decompressed it is ordinary carrier messages.
- **Frames.** The client's login is a reliable message on channel 0: `crc32(payload) u32 | len(payload) u32 | payload` (CRC-32/IEEE, big endian).
- **Payload.** 16 bytes of target actor id (all zero: the REP itself), then a `MessageEnvelope`. The reader is at `0x1461ac8d0`.
  - flags (u8): `1` → 8 more bytes, `2` → 8 more bytes, `4` → a routing block (`0x1461454d0`).
  - message present (u8).
  - type: a varint index into the client's type table (`GetTypeIdForIndex`). Index 0 is followed by the type's 16-byte UUID.
  - the message body.
- **Encoding** (`0x140876cd0` and its neighbours): u32/u64 are big endian. Strings are a varint length plus bytes. The varint's first byte gives its length: `0xxxxxxx`, `10xxxxxx`+1, `110xxxxx`+2, `1110xxxx`+3, `11110xxx`+4, low bits first (`0x140877970`).
- **The login** is `RegistrationRequestV2Msg` (type index 2, UUID `DA4E5889-A65C-4480-8642-0278160125A7`; see "Type table" below). Its body:
  - u32 `0x970c0a5d`;
  - the build as a map: varint count 6, then (u32 key, string) pairs: 4 `6031`, 3 `400`, 2 `1`, 1 `Javelin`, 5 `6004151`, 0 `[RETAIL]`;
  - an empty string;
  - the login token: signature, rep_endpoint, world_id, character_id, persona_id, zeros, client capabilities `rpc-channel`, ticket_id, then zeros/defaults.
- **The answer** the client waits for is `RegistrationResponseMsg` (UUID `104145a7-ff95-44f1-9468-21fb41c8ac2b`). Its marshal is `0x1407cceb0`, unmarshal `0x1407cd040`, and handler `0x146b6f190` ("Client connection is authorized"). Fields:
  - u32 result: 0 = authorized; otherwise an `mm_authresult_Denied_*` reason.
  - u64 server clock in nanoseconds, used for a clock offset.
  - string, kept by the client at +0x598.
  - string: the server version, which reaches `GameConnectionWrapper: received registration response from REP`.
  - four bools. The fourth makes the client start Easy Anti-Cheat; it must be 0.

`rep.go` sends this answer by type UUID (index 0), so no type table is needed.

### The two directions are framed differently (2026-10-23)

Test of 21:52: the client dropped the connection 9 ms after our first answer with "Server data error. Please check for a patch. (0)". That error is fatal error 10, which `0x146b6eb70` raises when a message fails to unmarshal before authorization.

- **Client → REP.** The marshaller (vtable `0x148590be8`, `0x146b0f430`) reserves 8 bytes, writes the 16-byte target actor id (slot 4, `0x146b0f640`) and the envelope, then fills in `crc32 | len` (slot 3, `0x146b0f350`, CRC `0x1412f47d0`).
- **REP → client.** Data from the transport (`0x146b714d0` → `0x146b6ba90` → `0x146af1d90`) is a byte stream. The parser `0x146ae44f0` reads `varint length | MessageEnvelope` frames and hands each to `0x146b6eb70`. There is no CRC and no actor id.

So the first answer, sent in the client → REP framing, was misread. The server now frames its messages as `varint length | envelope`.

### Type table: typeindex.json (2026-10-23)

The varint type index in an envelope is a position in `typeindex.json`, which ships next to `Bin64` in the game folder: `{"generationTime": "2025-10-22T03:13:16Z", "typeIndex": [7052 UUIDs]}`. The client loads it at start (`Loading typeindex generated on`), `GetTypeIdForIndex` (`0x1461acfe0`) looks entries up, and index 0 means a 16-byte UUID follows instead. The REP can refuse a client whose table differs (`@mm_authresult_Denied_TypeIndexMismatch`).

Each type registers itself with its name and UUID as strings, so `tools/re/typenames.py` names 308 of the entries and writes them to `rep_types.go` for the log. The first ones:

| Index | Type |
|---|---|
| 1 | `RegistrationRequestMsg` |
| 2 | `RegistrationRequestV2Msg`: **what the client sends** (earlier notes called it V3) |
| 3 | `RegistrationResponseMsg` (`104145A7-…`, what we answer) |
| 4, 5, 6 | `IdentifierMsg`, `IdentifierAckMsg`, `IdentifierCompleteMsg` |
| 7 | `FragmentUpdatesMsg` |
| 19 | `RegistrationRequestV3Msg` (`0B826B33-…`) |

The rest include `SpawnActorsMsg` (574), `TimeSynchMsg` (335), `PingMsg` (349), `KickClientMsg` (271), `ClientConnectionMsg` (374) and the fragment and actor messages, which are probably what follows authorization.

## After authorization: the client's GameConnection states (2026-10-23)

**The game's own log** is the best guide: `%LOCALAPPDATA%\AGS\New World\Game.log` (older runs in `LogBackups\`). It names every state change and every message it fails to read, for example `Failed to unmarshal MessageEnvelope::m_message. {"unmarshalError":"BufferOverrun","messageTypeId":"…"}`.

The states after the REP authorizes the client (`0x14644a070`), with what moves each one on:

| State | Waits for | Status |
|---|---|---|
| WaitingForREPConnection | `RegistrationResponseMsg` | done |
| WaitingForActorGameConnection | `Javelin::ClientMessagesTrait::PlayerManagerSelfIdentificationMsg` (index 1628): "actor game connection succeeds" | done, all fields empty |
| WaitingForSpawnPoint | `Javelin::ClientViewListenerTrait::ReceivePlayerSpawnPointMsg` (index 1617, empty body): "spawn point found" | done |
| WaitingForPlayerSpawn | the actor game's flag `+0x252` (`0x145a923c0`) and a local player with an entity: "player spawn succeeds, in game" | next |
| InGame | | |

A state that waits too long ends with `Errors::connection_game_timeout` ("Timed out while waiting for gameplay on the server", about 2 minutes).

After authorization, every REP → client type that the REP connection doesn't handle itself (`0x146b6f190` handles RegistrationResponse, TimeSynch, Ping, hub info, ClientAdd/RemoveEntry, Update and the text filters) is queued for the game (`0x146b6bf50`). No target actor or routing block is needed: `repEnvelope` (flags 0, type by UUID) works.

`PlayerManagerSelfIdentificationMsg` body (factory unmarshal `0x1414fd530`):

```
compact string                                  flags u8; bit 0 -> 16 UUID bytes, else varint-length string
3 x { u32, 16 bytes, 16 bytes }                 probably actor ids
u32 | varint n, n x u32 | bool | bool | f32 | f32
compact string
```

**Finding a message's layout.** A Hub message registers through `Amazon::Hub::InstallRegistrationHook<Msg>`. `tools/re/msginfo.py <MsgName> [Trait]` follows its lambda's RTTI to the type getter, which gives the UUID (and so the type index), and the factory, whose slot 5 is the unmarshal function. Read that function, not the message vtable: the first attempt here read a neighbouring class and got a 4-byte body the client rejected as BufferOverrun.

**Running a test without the launcher's window.** The game must be started from the launcher with Steam already running; otherwise pressing Play makes it relaunch through Steam ("Steam must be running"). After a server restart the game retries the REP login by itself.
