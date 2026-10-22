# Quintessence stub server

The first step towards a server for the Quintessence launcher. It does **not** play the game yet. It sits where the launcher points the New World client and records everything the client sends, so we can learn the protocol.

## What we know so far

- **The gateway is gRPC.** The game opens a **TCP** connection and speaks HTTP/2 in cleartext (h2c, no TLS). Its first settings frame carries setting `0xFE03`, which only Google's C++ gRPC library sends. (First capture, 2026-10-22.)
- With no HTTP/2 answer, the client hangs up after 20 seconds and retries.
- **The first call is `/Javelin.RPC.StubbedGatewayService/GetLoginInfo`** (user agent `grpc-c++/1.33.1`). Its request is `{1: {1: "<player name>"}, 2: "dev", 3: 1}`: the persona from the launcher's patch #4 and the `-ProductId dev` from its command line. Answered Unimplemented, the client asks again every 2 seconds. (Second capture, 2026-10-22.)
- **The gateway's messages carry no schema in the exe.** `protoextract` found only one schema, `CampfireEventDefault` (a telemetry event); the `Javelin.RPC` messages were built with the protobuf lite runtime. Their method paths and type names still are in the exe, and `protoextract` now lists them in `NAMES.txt`.

## What it does

- Listens on **UDP and TCP** on one port (default `6000`).
- A TCP connection that opens with the HTTP/2 preface goes to a **gRPC server that accepts any method**. It logs the method name (for example `/package.Service/Method`), the metadata and every message, decoded as protobuf by field number since we have no schema. Once the client has sent everything, it answers with an **empty message and OK** (`-reply empty`, the default). An empty message is valid for every message type, with every field at its default, so the client takes it as a success and shows us its next step. `-reply unimplemented` answers `Unimplemented` instead.
- Logs every packet, both directions, as a hex dump. DTLS records are decoded, and the client's `ClientHello` (version, cipher suites, extensions) is shown in full.
- When the client speaks DTLS, it **completes the handshake** with a fresh self-signed certificate. The launcher's patch #1 makes the client accept it. After that it logs the client's messages **decrypted**.
- It never answers application data, because we don't know the protocol yet. That's what the captures are for.

Each run writes three files to `captures\`:

| File | What it is |
|---|---|
| `session-<time>.log` | Text log: every packet, decoded and hex-dumped. This is the one to read and share. |
| `session-<time>.pcap` | The same traffic for Wireshark (it decodes HTTP/2 and gRPC on its own after *Decode As… → HTTP2*). |
| `session-<time>.keys` | DTLS session keys (NSS key log), so Wireshark can decrypt the `.pcap`. |

## Running it

On the PC where you play:

1. Start `quintessence-server.exe`. When Windows Firewall asks, allow it (private networks are enough).
2. In the launcher's Settings, set **Server** to `127.0.0.1:6000`.
3. Press **Play**. Wait until the game gives up connecting (or close it), then press **Ctrl+C** in the server window.
4. Look in `captures\` for the `.log` file.

Options:

```
quintessence-server.exe -listen 0.0.0.0:6000   address and port, UDP and TCP
                        -captures captures      folder for the capture files
                        -cert rsa|ecdsa         certificate key for the DTLS handshake (default rsa)
                        -no-dtls                only record, never answer the handshake
                        -reply empty|unimplemented  how to answer gRPC calls (default empty)
```

If the server can't start, the reason is in the `.log` and stays on screen until you press Enter. "Is another server already running on this port?" means an older server window is still open: close it. A warning about **no UDP** is harmless: some Windows setups (network filters from antivirus or VPN software) refuse a UDP socket option Go sets, and the server carries on with TCP, which is what the gateway uses. If the server ever dies, the reason is in `session-<time>.crash.txt`.

If the log says the handshake failed, try `-cert ecdsa`. If nothing arrives at all, the client may be using a different port, or it never got as far as connecting. Check that the launcher's Server setting matches `-listen`.

## protoextract: the game's own message definitions

Programs built with the full C++ protobuf library carry their `.proto` schemas inside the `.exe`. `protoextract` finds them and writes them back out as `.proto` files. It only reads the file it's given.

```
quintessence-protoextract.exe "F:\SteamLibrary\steamapps\common\New World\Bin64\NewWorld.exe"
```

Double-click works too: it then takes `NewWorld.exe` from the New World folder saved in the launcher. It writes a `protos` folder next to itself: one `.proto` per schema, `ALL.txt` with all of them in one file, `descriptors.pb` (a FileDescriptorSet), and `NAMES.txt`, the gRPC method paths and `Javelin` type names found as strings, which survive even when the schemas don't. Library schemas (`google/protobuf`, `grpc`) are skipped unless you pass `-all`.

## Reading the .pcap in Wireshark

Open the `.pcap`. Then:

- Right-click a packet → **Decode As…** → UDP port 6000 → **DTLS**.
- Edit → Preferences → Protocols → **TLS** → *(Pre)-Master-Secret log filename* → pick the `.keys` file.

The application data then shows as *Decrypted DTLS*.

## Building

Go 1.26 or newer:

```
go test ./...
GOOS=windows GOARCH=amd64 go build -o quintessence-server.exe .
GOOS=windows GOARCH=amd64 go build -o quintessence-protoextract.exe ./cmd/protoextract
```
