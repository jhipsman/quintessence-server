package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Capture writes everything the server sees to three files next to each other:
//
//	<base>.log   text: every packet with a hex dump and, for DTLS, a decode
//	<base>.pcap  the same packets for Wireshark (raw IP, synthesized headers)
//	<base>.keys  DTLS session keys in NSS key log format (Wireshark: TLS
//	             protocol preferences, "(Pre)-Master-Secret log filename")
//
// Everything that goes to the log also goes to the console.
type Capture struct {
	mu      sync.Mutex
	log     *os.File
	pcap    *os.File
	keys    *os.File
	console io.Writer
	tcpSeq  map[string]uint32 // next TCP sequence number per direction, for the pcap
}

func NewCapture(base string) (*Capture, error) {
	c := &Capture{console: os.Stdout, tcpSeq: map[string]uint32{}}
	var err error
	if c.log, err = os.Create(base + ".log"); err != nil {
		return nil, err
	}
	if c.pcap, err = os.Create(base + ".pcap"); err != nil {
		c.Close()
		return nil, err
	}
	if c.keys, err = os.Create(base + ".keys"); err != nil {
		c.Close()
		return nil, err
	}
	// pcap global header: version 2.4, snaplen 65535, LINKTYPE_RAW (101): each
	// packet starts with an IPv4 or IPv6 header.
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(hdr[4:], 2)
	binary.LittleEndian.PutUint16(hdr[6:], 4)
	binary.LittleEndian.PutUint32(hdr[16:], 65535)
	binary.LittleEndian.PutUint32(hdr[20:], 101)
	if _, err := c.pcap.Write(hdr); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Capture) Close() {
	for _, f := range []*os.File{c.log, c.pcap, c.keys} {
		if f != nil {
			f.Close()
		}
	}
}

// KeyLog is where the DTLS library writes the session keys.
func (c *Capture) KeyLog() io.Writer { return lockedWriter{c} }

type lockedWriter struct{ c *Capture }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.c.keys.Write(p)
}

// Recover logs a panic in one connection's handler instead of letting it take
// the whole server down. Use it as `defer cap.Recover("where")`.
func (c *Capture) Recover(where string) {
	if r := recover(); r != nil {
		c.Notef("PANIC in %s (the server keeps running): %v\n%s", where, r, bytes.TrimSpace(debug.Stack()))
	}
}

// Notef logs a line of text.
func (c *Capture) Notef(format string, args ...any) {
	c.write(stamp(time.Now()) + "  " + fmt.Sprintf(format, args...) + "\n")
}

// Packet records one datagram (UDP) or one chunk of a stream (TCP) as it went
// over the wire. client is the game's address, server ours.
func (c *Capture) Packet(proto string, client, server netip.AddrPort, fromClient bool, data []byte) {
	now := time.Now()
	dir := "client -> server"
	if !fromClient {
		dir = "server -> client"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  %s  %s  %d bytes\n", stamp(now), proto, client, dir, len(data))
	if proto == "UDP" {
		for _, line := range DescribeDTLS(data) {
			b.WriteString("    " + line + "\n")
		}
	} else if line := describeStream(data); line != "" {
		b.WriteString("    " + line + "\n")
	}
	b.WriteString(indent(hex.Dump(data)))
	c.write(b.String())

	src, dst := client, server
	if !fromClient {
		src, dst = server, client
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writePcap(now, proto, src, dst, data)
}

// PcapOnly records traffic in the pcap but not in the text log, for streams
// the log shows at a higher level (gRPC calls).
func (c *Capture) PcapOnly(proto string, client, server netip.AddrPort, fromClient bool, data []byte) {
	src, dst := client, server
	if !fromClient {
		src, dst = server, client
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writePcap(time.Now(), proto, src, dst, data)
}

// Decrypted records application data after the DTLS layer took it apart.
func (c *Capture) Decrypted(client netip.AddrPort, fromClient bool, data []byte) {
	dir := "client -> server"
	if !fromClient {
		dir = "server -> client"
	}
	c.write(fmt.Sprintf("%s  DTLS-APP  %s  %s  %d bytes DECRYPTED\n%s", stamp(time.Now()), client, dir, len(data), indent(hex.Dump(data))))
}

func (c *Capture) write(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	io.WriteString(c.console, s)
	io.WriteString(c.log, s)
}

func stamp(t time.Time) string { return t.Format("15:04:05.000") }

func indent(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return "    " + strings.ReplaceAll(s, "\n", "\n    ") + "\n"
}

// describeStream names the obvious things a TCP client might open with.
func describeStream(b []byte) string {
	switch {
	case strings.HasPrefix(string(b), "PRI * HTTP/2.0"):
		return "HTTP/2 connection preface (cleartext h2c)"
	case len(b) >= 3 && b[0] == 0x16 && b[1] == 0x03:
		return "looks like a TLS handshake (TCP)"
	case len(b) >= 4 && (string(b[:4]) == "GET " || string(b[:4]) == "POST" || string(b[:4]) == "PUT " || string(b[:4]) == "HEAD"):
		line, _, _ := strings.Cut(string(b), "\r\n")
		return "HTTP request: " + line
	}
	return ""
}

// ---- pcap ----------------------------------------------------------------

func (c *Capture) writePcap(t time.Time, proto string, src, dst netip.AddrPort, payload []byte) {
	var l4 []byte
	var ipProto byte
	if proto == "UDP" {
		ipProto = 17
		l4 = make([]byte, 8, 8+len(payload))
		binary.BigEndian.PutUint16(l4[0:], src.Port())
		binary.BigEndian.PutUint16(l4[2:], dst.Port())
		binary.BigEndian.PutUint16(l4[4:], uint16(8+len(payload)))
	} else {
		ipProto = 6
		key := src.String() + ">" + dst.String()
		seq := c.tcpSeq[key]
		ack := c.tcpSeq[dst.String()+">"+src.String()]
		c.tcpSeq[key] = seq + uint32(len(payload))
		l4 = make([]byte, 20, 20+len(payload))
		binary.BigEndian.PutUint16(l4[0:], src.Port())
		binary.BigEndian.PutUint16(l4[2:], dst.Port())
		binary.BigEndian.PutUint32(l4[4:], seq)
		binary.BigEndian.PutUint32(l4[8:], ack)
		l4[12] = 5 << 4 // header length: 5 words
		l4[13] = 0x18   // PSH, ACK
		binary.BigEndian.PutUint16(l4[14:], 65535)
	}
	l4 = append(l4, payload...)

	s, d := src.Addr().Unmap(), dst.Addr().Unmap()
	if s.Is4() != d.Is4() { // mixed families: show both as IPv6
		s, d = netip.AddrFrom16(s.As16()), netip.AddrFrom16(d.As16())
	}
	var ip []byte
	if s.Is4() {
		ip = make([]byte, 20)
		ip[0] = 0x45
		binary.BigEndian.PutUint16(ip[2:], uint16(20+len(l4)))
		ip[8] = 64
		ip[9] = ipProto
		s4, d4 := s.As4(), d.As4()
		copy(ip[12:], s4[:])
		copy(ip[16:], d4[:])
		binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip))
	} else {
		ip = make([]byte, 40)
		ip[0] = 0x60
		binary.BigEndian.PutUint16(ip[4:], uint16(len(l4)))
		ip[6] = ipProto
		ip[7] = 64
		s16, d16 := s.As16(), d.As16()
		copy(ip[8:], s16[:])
		copy(ip[24:], d16[:])
	}
	pkt := append(ip, l4...)

	rec := make([]byte, 16)
	binary.LittleEndian.PutUint32(rec[0:], uint32(t.Unix()))
	binary.LittleEndian.PutUint32(rec[4:], uint32(t.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(rec[8:], uint32(len(pkt)))
	binary.LittleEndian.PutUint32(rec[12:], uint32(len(pkt)))
	c.pcap.Write(rec)
	c.pcap.Write(pkt)
}

func ipChecksum(h []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(h); i += 2 {
		sum += uint32(h[i])<<8 | uint32(h[i+1])
	}
	for sum > 0xffff {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// addrPort turns a net.Addr from a socket into a netip.AddrPort.
func addrPort(a net.Addr) netip.AddrPort {
	switch v := a.(type) {
	case *net.UDPAddr:
		return v.AddrPort()
	case *net.TCPAddr:
		return v.AddrPort()
	}
	ap, _ := netip.ParseAddrPort(a.String())
	return ap
}
