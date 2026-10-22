package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"
)

// h2Preface opens every HTTP/2 connection; the game's gRPC client sends it
// straight away (cleartext, prior knowledge).
const h2Preface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// sniffTimeout is how long a new connection may take to send its first bytes.
const sniffTimeout = 5 * time.Second

// TCPServer accepts connections on the same port as the UDP side. A
// connection that opens with the HTTP/2 preface goes to the gRPC server;
// anything else is logged byte for byte and never answered.
type TCPServer struct {
	ln   net.Listener
	cap  *Capture
	grpc *GRPCServer
}

func ListenTCP(addr string, cap *Capture) (*TCPServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &TCPServer{ln: ln, cap: cap, grpc: NewGRPCServer(cap, ln.Addr())}, nil
}

func (s *TCPServer) Close() error {
	s.grpc.Close()
	return s.ln.Close()
}

func (s *TCPServer) Serve() {
	go s.grpc.Serve()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.cap.Notef("TCP accept error: %v", err)
			continue
		}
		go s.handle(c)
	}
}

func (s *TCPServer) handle(c net.Conn) {
	defer s.cap.Recover("a TCP connection")
	client, local := addrPort(c.RemoteAddr()), addrPort(c.LocalAddr())
	client = netip.AddrPortFrom(client.Addr().Unmap(), client.Port())
	s.cap.Notef("TCP %s: connected", client)

	// Sniff: wait (briefly) for the preface's length. A client that sends less
	// and waits is logged raw from whatever arrived.
	br := bufio.NewReaderSize(c, 65536)
	c.SetReadDeadline(time.Now().Add(sniffTimeout))
	head, err := br.Peek(len(h2Preface))
	c.SetReadDeadline(time.Time{})
	if err == nil && string(head) == h2Preface {
		s.cap.Notef("TCP %s: HTTP/2 preface, handing the connection to the gRPC server", client)
		s.grpc.Add(&recordedConn{Conn: c, r: br, cap: s.cap, client: client, local: local})
		return
	}

	defer c.Close()
	if len(head) > 0 { // the bytes Peek saw are still in br and get logged below
		s.cap.Notef("TCP %s: not HTTP/2, logging raw bytes", client)
	}
	s.logRaw(br, c, client, local)
}

func (s *TCPServer) logRaw(r io.Reader, c net.Conn, client, local netip.AddrPort) {
	buf := make([]byte, 65536)
	for {
		c.SetReadDeadline(time.Now().Add(peerIdle))
		n, err := r.Read(buf)
		if n > 0 {
			s.cap.Packet("TCP", client, local, true, append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			s.cap.Notef("TCP %s: closed (%v)", client, err)
			return
		}
	}
}
