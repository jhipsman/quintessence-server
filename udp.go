package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
)

const (
	handshakeTimeout = 30 * time.Second
	peerIdle         = 2 * time.Minute // a client that sends nothing this long is forgotten
)

// DTLSSetup is the server side of the DTLS handshake.
type DTLSSetup struct{ cfg *dtls.Config }

// NewDTLSSetup makes a fresh self-signed certificate. The client checks
// nothing about it: the launcher's patch #1 makes it accept any certificate.
func NewDTLSSetup(certType string, keyLog io.Writer) (*DTLSSetup, error) {
	var key any
	var pub any
	switch certType {
	case "rsa":
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		key, pub = k, &k.PublicKey
	case "ecdsa":
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		key, pub = k, &k.PublicKey
	default:
		return nil, fmt.Errorf("unknown -cert %q (use rsa or ecdsa)", certType)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "quintessence-stub"},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		return nil, err
	}
	return &DTLSSetup{cfg: &dtls.Config{
		Certificates:         []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		ExtendedMasterSecret: dtls.RequestExtendedMasterSecret,
		KeyLogWriter:         keyLog,
	}}, nil
}

// UDPServer reads every datagram, logs it and hands it to the peer it came
// from. With DTLS on, each peer gets its own DTLS server connection, which
// reads from the peer's queue and writes through the shared socket.
type UDPServer struct {
	conn  *net.UDPConn
	cap   *Capture
	dtls  *DTLSSetup
	local netip.AddrPort

	mu      sync.Mutex
	peers   map[netip.AddrPort]*peerConn
	ackOnly int
}

func ListenUDP(addr *net.UDPAddr, cap *Capture, d *DTLSSetup) (*UDPServer, error) {
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	return &UDPServer{conn: conn, cap: cap, dtls: d, local: addrPort(conn.LocalAddr()), peers: map[netip.AddrPort]*peerConn{}}, nil
}

func (s *UDPServer) Close() error { return s.conn.Close() }

func (s *UDPServer) Serve() {
	buf := make([]byte, 65536)
	for {
		n, from, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.cap.Notef("UDP read error: %v", err) // e.g. ICMP port unreachable on Windows
			continue
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		s.datagram(from, append([]byte(nil), buf[:n]...))
	}
}

func (s *UDPServer) datagram(from netip.AddrPort, data []byte) {
	defer s.cap.Recover("a UDP datagram")
	s.cap.Packet("UDP", from, s.local, true, data)
	if s.dtls != nil {
		s.peer(from, data).deliver(data)
	}
}

// peer finds the connection for an address, or starts one when the datagram
// opens a DTLS handshake (a ClientHello).
func (s *UDPServer) peer(addr netip.AddrPort, first []byte) *peerConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.peers[addr]; ok {
		return p
	}
	p := newPeerConn(s, addr)
	if !isClientHello(first) {
		return p // not tracked: the datagram is only logged
	}
	s.peers[addr] = p
	s.cap.Notef("UDP %s: DTLS ClientHello, starting the handshake", addr)
	go s.serveDTLS(p)
	return p
}

func (s *UDPServer) forget(p *peerConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peers[p.addr] == p {
		delete(s.peers, p.addr)
	}
}

func (s *UDPServer) serveDTLS(p *peerConn) {
	defer s.cap.Recover("a DTLS connection")
	defer p.Close()
	conn, err := dtls.Server(p, net.UDPAddrFromAddrPort(p.addr), s.dtls.cfg)
	if err != nil {
		s.cap.Notef("UDP %s: DTLS setup failed: %v", p.addr, err)
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	err = conn.HandshakeContext(ctx)
	cancel()
	if err != nil {
		s.cap.Notef("UDP %s: DTLS handshake FAILED: %v", p.addr, err)
		return
	}
	state, _ := conn.ConnectionState()
	s.cap.Notef("UDP %s: DTLS handshake COMPLETE, cipher suite %s. Everything below is decrypted.", p.addr, dtls.CipherSuiteName(state.CipherSuiteID))

	cs := newCarrierSession()
	var wmu sync.Mutex
	send := func(out [][]byte) {
		wmu.Lock()
		defer wmu.Unlock()
		for _, d := range out {
			s.logCarrier(p.addr, false, d)
			if _, err := conn.Write(d); err != nil {
				s.cap.Notef("UDP %s: DTLS write failed: %v", p.addr, err)
			}
		}
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer s.cap.Recover("the carrier timer")
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				send(cs.Tick())
			}
		}
	}()

	buf := make([]byte, 65536)
	for {
		conn.SetReadDeadline(time.Now().Add(peerIdle))
		n, err := conn.Read(buf)
		if err != nil {
			s.cap.Notef("UDP %s: DTLS connection ended: %v", p.addr, err)
			return
		}
		data := append([]byte(nil), buf[:n]...)
		s.logCarrier(p.addr, true, data)
		out, note := cs.Receive(data)
		if note != "" {
			s.cap.Notef("UDP %s:\n%s", p.addr, note)
		}
		send(out)
	}
}

// logCarrier logs one decrypted datagram. Datagrams that only carry acks go
// to the pcap and are counted, or keep-alives would flood the log.
func (s *UDPServer) logCarrier(addr netip.AddrPort, fromClient bool, data []byte) {
	d, err := parseCarrierDatagram(data)
	if err == nil && onlyAcks(d) {
		s.mu.Lock()
		s.ackOnly++
		n := s.ackOnly
		s.mu.Unlock()
		if n%200 == 0 {
			s.cap.Notef("UDP %s: %d ack-only carrier datagrams so far (not logged one by one)", addr, n)
		}
		return
	}
	s.cap.Decrypted(addr, fromClient, data)
	if err != nil {
		s.cap.Notef("    carrier: cannot parse: %v", err)
		return
	}
	s.cap.Notef("%s", describeCarrier(d))
}

// isClientHello says whether a datagram starts with a DTLS handshake record
// holding a ClientHello.
func isClientHello(b []byte) bool {
	return len(b) >= 14 && b[0] == 22 && b[1] == 0xfe && b[13] == 1
}

// peerConn is a net.PacketConn that only sees one client's datagrams. The DTLS
// library reads and writes through it; writes are logged on the way out.
type peerConn struct {
	srv  *UDPServer
	addr netip.AddrPort
	in   chan []byte

	closeOnce sync.Once
	closed    chan struct{}

	mu       sync.Mutex
	deadline time.Time
}

func newPeerConn(s *UDPServer, addr netip.AddrPort) *peerConn {
	return &peerConn{srv: s, addr: addr, in: make(chan []byte, 256), closed: make(chan struct{})}
}

func (p *peerConn) deliver(b []byte) {
	select {
	case p.in <- b:
	case <-p.closed:
	default: // queue full: drop, as the network would
	}
}

func (p *peerConn) ReadFrom(b []byte) (int, net.Addr, error) {
	p.mu.Lock()
	dl := p.deadline
	p.mu.Unlock()
	var timeout <-chan time.Time
	if !dl.IsZero() {
		t := time.NewTimer(time.Until(dl))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case d := <-p.in:
		return copy(b, d), net.UDPAddrFromAddrPort(p.addr), nil
	case <-p.closed:
		return 0, nil, net.ErrClosed
	case <-timeout:
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (p *peerConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	select {
	case <-p.closed:
		return 0, net.ErrClosed
	default:
	}
	p.srv.cap.Packet("UDP", p.addr, p.srv.local, false, append([]byte(nil), b...))
	return p.srv.conn.WriteToUDPAddrPort(b, p.addr)
}

func (p *peerConn) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.srv.forget(p)
	})
	return nil
}

func (p *peerConn) LocalAddr() net.Addr { return p.srv.conn.LocalAddr() }

func (p *peerConn) SetDeadline(t time.Time) error { return p.SetReadDeadline(t) }

func (p *peerConn) SetReadDeadline(t time.Time) error {
	p.mu.Lock()
	p.deadline = t
	p.mu.Unlock()
	return nil
}

func (p *peerConn) SetWriteDeadline(time.Time) error { return nil }
