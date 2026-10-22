// Quintessence stub server: the first step towards a New World server.
//
// It does not play the game. It listens where the launcher points the client
// (-GatewayAddr host:port), on UDP and on TCP, and records everything the
// client sends:
//
//   - every datagram and every TCP chunk, both ways, as a hex dump in a text
//     log, with DTLS records decoded (the ClientHello in full);
//   - the same traffic as a .pcap that Wireshark opens;
//   - when the client speaks DTLS, the server completes the handshake with a
//     self-signed certificate (the launcher's patch #1 makes the client accept
//     it) and logs the client's first DECRYPTED messages. The session keys go
//     to a key log file, so Wireshark can decrypt the .pcap too.
//
// It never answers application data: we don't know the protocol yet. That is
// what the captures are for.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

func main() {
	listen := flag.String("listen", "0.0.0.0:6000", "address to listen on, UDP and TCP (the launcher's Server setting must use the same port)")
	dir := flag.String("captures", "captures", "folder for the capture files")
	noDTLS := flag.Bool("no-dtls", false, "only record the client's packets, never answer the DTLS handshake")
	certType := flag.String("cert", "rsa", "certificate key for the DTLS handshake: rsa or ecdsa")
	flag.Parse()

	if err := run(*listen, *dir, !*noDTLS, *certType); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(listen, dir string, dtlsOn bool, certType string) error {
	stamp := time.Now().Format("2006-01-02_15-04-05")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := filepath.Join(dir, "session-"+stamp)
	cap, err := NewCapture(base)
	if err != nil {
		return err
	}
	defer cap.Close()

	udpAddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	var tlsCfg *DTLSSetup
	if dtlsOn {
		tlsCfg, err = NewDTLSSetup(certType, cap.KeyLog())
		if err != nil {
			return err
		}
	}

	udp, err := ListenUDP(udpAddr, cap, tlsCfg)
	if err != nil {
		return fmt.Errorf("UDP %s: %w", listen, err)
	}
	defer udp.Close()
	tcp, err := ListenTCP(listen, cap)
	if err != nil {
		return fmt.Errorf("TCP %s: %w", listen, err)
	}
	defer tcp.Close()

	mode := "record + DTLS handshake (" + certType + " certificate)"
	if !dtlsOn {
		mode = "record only"
	}
	cap.Notef("Quintessence stub server listening on %s (UDP and TCP), %s", listen, mode)
	cap.Notef("Writing %s.log, %s.pcap and %s.keys", base, base, base)
	cap.Notef("Point the launcher's Server setting at this PC and this port, press Play, then Ctrl+C here when the game gives up.")

	go udp.Serve()
	go tcp.Serve()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	cap.Notef("Stopping. Send %s.log (and the .pcap and .keys if you can) for analysis.", base)
	return nil
}
