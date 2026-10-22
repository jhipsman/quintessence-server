# Quintessence stub server

The first step towards a server for the Quintessence launcher. It does **not** play the game yet. It sits where the launcher points the New World client and records everything the client sends, so we can learn the protocol.

## What we know so far

- **The gateway is gRPC.** The game opens a **TCP** connection and speaks HTTP/2 in cleartext (h2c, no TLS). Its first settings frame carries setting `0xFE03`, which only Google's C++ gRPC library sends. (First capture, 2026-10-22.)
- With no HTTP/2 answer, the client hangs up after 20 seconds and retries.

## What it does

- Listens on **UDP and TCP** on one port (default `6000`).
- A TCP connection that opens with the HTTP/2 preface goes to a **gRPC server that accepts any method**. It logs the method name (for example `/package.Service/Method`), the metadata and every message, decoded as protobuf by field number since we have no schema. Once the client has sent everything, it answers `Unimplemented`, so the client gets a reply and moves on to its next call.
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
```

If the log says the handshake failed, try `-cert ecdsa`. If nothing arrives at all, the client may be using a different port, or it never got as far as connecting. Check that the launcher's Server setting matches `-listen`.

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
```
