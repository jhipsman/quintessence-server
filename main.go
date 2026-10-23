// Quintessence stub server: the first step towards a New World server.
//
// It does not play the game. It listens where the launcher points the client
// (-GatewayAddr host:port), on UDP and on TCP, and records everything the
// client sends:
//
//   - gRPC calls: the gateway client is C++ gRPC over cleartext HTTP/2, so
//     TCP connections that open with the HTTP/2 preface go to a gRPC server
//     that accepts any method and logs it with its metadata and messages;
//   - every datagram and every other TCP chunk, both ways, as a hex dump in
//     a text log, with DTLS records decoded (the ClientHello in full);
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
	"runtime/debug"
	"time"
)

func main() {
	listen := flag.String("listen", "0.0.0.0:6000", "address to listen on, UDP and TCP (the launcher's Server setting must use the same port)")
	dir := flag.String("captures", "captures", "folder for the capture files")
	noDTLS := flag.Bool("no-dtls", false, "only record the client's packets, never answer the DTLS handshake")
	certType := flag.String("cert", "rsa", "certificate key for the DTLS handshake: rsa or ecdsa")
	reply := flag.String("reply", "gateway", "how to answer gRPC calls: gateway (real login-flow answers), probe3, probe2 or probe (GetLoginInfo gets a probe message, the rest an empty one), empty, or unimplemented")
	flag.Parse()

	switch *reply {
	case "gateway", "probe3", "probe2", "probe", "empty", "unimplemented":
	default:
		fmt.Fprintln(os.Stderr, "error: -reply must be gateway, probe3, probe2, probe, empty or unimplemented")
		os.Exit(2)
	}
	if err := run(*listen, *dir, !*noDTLS, *certType, *reply); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		// keep the window open when the server was started by double-click
		fmt.Fprintln(os.Stderr, "Press Enter to close.")
		fmt.Scanln()
		os.Exit(1)
	}
}

func run(listen, dir string, dtlsOn bool, certType string, reply string) error {
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

	// If the server dies anyway, Go writes the reason and the stack here.
	crashPath := base + ".crash.txt"
	if f, err := os.Create(crashPath); err == nil {
		debug.SetCrashOutput(f, debug.CrashOptions{})
		f.Close()
		defer func() { // a clean stop leaves no empty crash file behind
			debug.SetCrashOutput(nil, debug.CrashOptions{}) // release it (Windows can't delete open files)
			if st, err := os.Stat(crashPath); err == nil && st.Size() == 0 {
				os.Remove(crashPath)
			}
		}()
	}

	// From here on, errors go into the log as well as the console: a run that
	// fails to start must not leave an empty log behind.
	fail := func(err error) error {
		cap.Notef("ERROR: %v", err)
		return err
	}

	udpAddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return fail(fmt.Errorf("listen address: %w", err))
	}
	var tlsCfg *DTLSSetup
	if dtlsOn {
		tlsCfg, err = NewDTLSSetup(certType, cap.KeyLog())
		if err != nil {
			return fail(err)
		}
	}

	tcp, err := ListenTCP(listen, cap)
	if err != nil {
		return fail(fmt.Errorf("TCP %s: %w (is another server already running on this port?)", listen, err))
	}
	defer tcp.Close()
	tcp.grpc.reply = reply
	if reply == "gateway" {
		gw, err := NewGateway(filepath.Join(dir, "..", "characters.json"))
		if err != nil {
			return fail(fmt.Errorf("characters: %w", err))
		}
		tcp.grpc.gw = gw
	}
	go tcp.Serve()

	// UDP is optional: the gateway is TCP. Some Windows setups (network filter
	// drivers from antivirus or VPN software, and Wine) refuse a socket option
	// Go sets on every UDP socket since Go 1.25, so carry on without it.
	transports := "TCP only"
	udp, err := ListenUDP(udpAddr, cap, tlsCfg)
	if err != nil {
		cap.Notef("WARNING: no UDP on %s (%v). Carrying on with TCP only, which is what the gateway uses.", listen, err)
	} else {
		defer udp.Close()
		go udp.Serve()
		transports = "UDP and TCP"
	}

	mode := "record + DTLS handshake (" + certType + " certificate) + carrier connect"
	if !dtlsOn {
		mode = "record only"
	}
	answer := map[string]string{
		"probe":         "GetLoginInfo gets the probe reply, other gRPC calls an empty one",
		"probe2":        "GetLoginInfo gets probe 2 (one id everywhere), other gRPC calls an empty one",
		"gateway":       "the login gateway answers for real (world \"Quintessence\", characters in characters.json)",
		"probe3":        "GetLoginInfo gets probe 3 (which character field is world_id), other gRPC calls an empty one",
		"empty":         "gRPC calls get an empty reply",
		"unimplemented": "gRPC calls get Unimplemented",
	}[reply]
	cap.Notef("Quintessence stub server listening on %s (%s), %s, %s", listen, transports, mode, answer)
	cap.Notef("Writing %s.log, %s.pcap and %s.keys", base, base, base)
	cap.Notef("Point the launcher's Server setting at this PC and this port, press Play, then Ctrl+C here when the game gives up.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	cap.Notef("Stopping. Send %s.log (and the .pcap and .keys if you can) for analysis.", base)
	return nil
}
