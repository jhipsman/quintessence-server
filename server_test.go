package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
)

func newTestCapture(t *testing.T) (*Capture, string) {
	t.Helper()
	base := filepath.Join(t.TempDir(), "session")
	c, err := NewCapture(base)
	if err != nil {
		t.Fatal(err)
	}
	c.console = io.Discard
	t.Cleanup(c.Close)
	return c, base
}

func readLog(t *testing.T, base string) string {
	t.Helper()
	b, err := os.ReadFile(base + ".log")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// waitFor polls the log until it holds want.
func waitFor(t *testing.T, base, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		log := readLog(t, base)
		if strings.Contains(log, want) {
			return log
		}
		if time.Now().After(deadline) {
			t.Fatalf("log never got %q:\n%s", want, log)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDTLSHandshakeAndDecryptedLog(t *testing.T) {
	for _, certType := range []string{"rsa", "ecdsa"} {
		t.Run(certType, func(t *testing.T) {
			cap, base := newTestCapture(t)
			setup, err := NewDTLSSetup(certType, cap.KeyLog())
			if err != nil {
				t.Fatal(err)
			}
			srv, err := ListenUDP(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, cap, setup)
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Close()
			go srv.Serve()

			// a client like the patched game: it accepts any certificate
			raddr := srv.conn.LocalAddr().(*net.UDPAddr)
			pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			client, err := dtls.Client(pc, raddr, &dtls.Config{InsecureSkipVerify: true})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := client.HandshakeContext(ctx); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			if _, err := client.Write([]byte("hello gateway")); err != nil {
				t.Fatal(err)
			}

			log := waitFor(t, base, "DECRYPTED")
			for _, want := range []string{
				"DTLS ClientHello, starting the handshake",
				"ClientHello (message 0",
				"cipher suites:",
				"HelloVerifyRequest",
				"ServerHello",
				"DTLS handshake COMPLETE",
				"|hello gateway|",
			} {
				if !strings.Contains(log, want) {
					t.Errorf("log lacks %q", want)
				}
			}
			keys, _ := os.ReadFile(base + ".keys")
			if !strings.HasPrefix(string(keys), "CLIENT_RANDOM ") {
				t.Errorf("key log = %q", keys)
			}
			pcap, _ := os.ReadFile(base + ".pcap")
			if len(pcap) <= 24 || binary.LittleEndian.Uint32(pcap) != 0xa1b2c3d4 {
				t.Errorf("pcap has no packets (%d bytes)", len(pcap))
			}
		})
	}
}

func TestNonDTLSDatagramIsOnlyLogged(t *testing.T) {
	cap, base := newTestCapture(t)
	setup, err := NewDTLSSetup("rsa", cap.KeyLog())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ListenUDP(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, cap, setup)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve()

	c, err := net.DialUDP("udp", nil, srv.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("GRIDMATE? hello there")); err != nil {
		t.Fatal(err)
	}
	log := waitFor(t, base, "|GRIDMATE? hello |")
	if !strings.Contains(log, "not DTLS (first byte 0x47") {
		t.Errorf("log:\n%s", log)
	}
	srv.mu.Lock()
	n := len(srv.peers)
	srv.mu.Unlock()
	if n != 0 {
		t.Errorf("a non-DTLS datagram started a DTLS peer")
	}
}

func TestTCPIsLogged(t *testing.T) {
	cap, base := newTestCapture(t)
	srv, err := ListenTCP("127.0.0.1:0", cap)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve()
	c, err := net.Dial("tcp", srv.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("GET /login HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, base, "HTTP request: GET /login HTTP/1.1")
}

func TestDescribeDTLSTruncated(t *testing.T) {
	got := strings.Join(DescribeDTLS([]byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 50, 1}), "\n")
	if !strings.Contains(got, "only 1 are here") {
		t.Errorf("got %q", got)
	}
	if got := DescribeDTLS([]byte{1, 2, 3}); !strings.Contains(got[0], "not DTLS: only 3 bytes") {
		t.Errorf("got %q", got)
	}
}
