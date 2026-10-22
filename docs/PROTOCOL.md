# What we know about the protocol

New World build `1.400.6031.6004151`, started by the Quintessence launcher with `-GatewayMode stubbed -GatewayAddr <host:port>`. Everything here comes from captures of the real client and from strings in `NewWorld.exe` (`NAMES-1.400.6031.txt`, written by `protoextract`). Field **names** are known only for string fields; field **numbers** are known only where a capture shows them.

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
| `GetLoginInfoResult` | none: it only holds non-string fields (probably lists of worlds and characters) |
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
4. Next: `-reply probe` answers `GetLoginInfo` with a message that fills fields 1-20, each with fields 1-15 holding labels of their position (`probe.go`), to see which fields the game shows as worlds and characters.
