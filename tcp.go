package main

import (
	"errors"
	"net"
	"net/netip"
	"time"
)

// TCPServer accepts connections on the same port and logs what the client
// sends. It never answers: it is only there in case the client opens its
// gateway connection over TCP instead of UDP.
type TCPServer struct {
	ln  net.Listener
	cap *Capture
}

func ListenTCP(addr string, cap *Capture) (*TCPServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &TCPServer{ln: ln, cap: cap}, nil
}

func (s *TCPServer) Close() error { return s.ln.Close() }

func (s *TCPServer) Serve() {
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
	defer c.Close()
	client, local := addrPort(c.RemoteAddr()), addrPort(c.LocalAddr())
	client = netip.AddrPortFrom(client.Addr().Unmap(), client.Port())
	s.cap.Notef("TCP %s: connected", client)
	buf := make([]byte, 65536)
	for {
		c.SetReadDeadline(time.Now().Add(peerIdle))
		n, err := c.Read(buf)
		if n > 0 {
			s.cap.Packet("TCP", client, local, true, append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			s.cap.Notef("TCP %s: closed (%v)", client, err)
			return
		}
	}
}
