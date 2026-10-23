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
                        4 repeated CharacterNameData; 5 uint32; 6 uint32;
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
- Useful console variables: `g_showHiddenWorlds`, `g_loginQueueBypass`, `g_maintenanceModeBypass`, `g_createCharacterAccountLimitBypass`, `g_selectWorldId`, `g_loginWithWorldTypes`.

Probe results, explained: probe 1's character pointed at world `@3@3@_@_` while the world's id was `@2@1@_@_` ("World Unavailable"); in probes 2 and 3 the ids matched but the world's version (`@W@W@W@W`) failed the version check, taking its characters with it.
