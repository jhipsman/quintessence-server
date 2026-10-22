package main

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/pion/dtls/v3"
)

// DescribeDTLS decodes the DTLS records in one datagram into readable lines.
// It never fails: whatever does not parse is reported as such, which is
// itself useful (a datagram that is not DTLS at all tells us the client
// speaks something else first).
func DescribeDTLS(b []byte) []string {
	var out []string
	for n := 0; len(b) > 0; n++ {
		if len(b) < 13 {
			if n == 0 {
				return append(out, fmt.Sprintf("not DTLS: only %d bytes, too short for a record header", len(b)))
			}
			return append(out, fmt.Sprintf("not DTLS after record %d: %d trailing bytes", n, len(b)))
		}
		ct, ver, epoch := b[0], binary.BigEndian.Uint16(b[1:3]), binary.BigEndian.Uint16(b[3:5])
		seq := uint64(b[5])<<40 | uint64(binary.BigEndian.Uint32(b[7:11])) | uint64(b[6])<<32
		ln := int(binary.BigEndian.Uint16(b[11:13]))
		if !knownContentType(ct) || ver>>8 != 0xfe {
			if n == 0 {
				return append(out, fmt.Sprintf("not DTLS (first byte 0x%02x, bytes 1-2 0x%04x)", ct, ver))
			}
			return append(out, fmt.Sprintf("not DTLS after record %d: %d bytes left", n, len(b)))
		}
		if 13+ln > len(b) {
			return append(out, fmt.Sprintf("DTLS record says %d bytes but only %d are here", ln, len(b)-13))
		}
		body := b[13 : 13+ln]
		b = b[13+ln:]
		head := fmt.Sprintf("DTLS %s record, %s, epoch %d, seq %d, %d bytes", versionName(ver), contentTypeName(ct), epoch, seq, ln)
		switch {
		case epoch > 0:
			out = append(out, head+" (encrypted)")
		case ct == 22:
			out = append(out, head)
			out = append(out, describeHandshake(body)...)
		case ct == 21 && len(body) >= 2:
			out = append(out, head+fmt.Sprintf(": alert %s %s", alertLevel(body[0]), alertName(body[1])))
		default:
			out = append(out, head)
		}
	}
	return out
}

func describeHandshake(b []byte) []string {
	if len(b) < 12 {
		return []string{"  handshake header too short"}
	}
	typ := b[0]
	total := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	msgSeq := binary.BigEndian.Uint16(b[4:6])
	off := int(b[6])<<16 | int(b[7])<<8 | int(b[8])
	fl := int(b[9])<<16 | int(b[10])<<8 | int(b[11])
	line := fmt.Sprintf("  %s (message %d, %d bytes", handshakeName(typ), msgSeq, total)
	if off != 0 || fl != total {
		line += fmt.Sprintf(", fragment %d..%d", off, off+fl)
	}
	line += ")"
	out := []string{line}
	if typ == 1 && off == 0 && fl == total && len(b) >= 12+fl {
		out = append(out, describeClientHello(b[12:12+fl])...)
	}
	return out
}

func describeClientHello(b []byte) []string {
	r := reader{b: b}
	ver := r.u16()
	r.skip(32) // random
	sid := r.vec8()
	cookie := r.vec8()
	suites := r.vec16()
	comp := r.vec8()
	if r.bad {
		return []string{"    ClientHello is truncated"}
	}
	out := []string{
		fmt.Sprintf("    client version %s, session id %d bytes, cookie %d bytes", versionName(ver), len(sid), len(cookie)),
		fmt.Sprintf("    %d cipher suites:", len(suites)/2),
	}
	for i := 0; i+1 < len(suites); i += 2 {
		id := binary.BigEndian.Uint16(suites[i:])
		out = append(out, fmt.Sprintf("      0x%04x %s", id, suiteName(id)))
	}
	out = append(out, fmt.Sprintf("    compression methods: % x", comp))
	if r.left() == 0 {
		return append(out, "    no extensions")
	}
	exts := reader{b: r.vec16()}
	out = append(out, "    extensions:")
	for exts.left() > 0 && !exts.bad {
		typ := exts.u16()
		data := exts.vec16()
		out = append(out, fmt.Sprintf("      %d %s (%d bytes)%s", typ, extName(typ), len(data), extDetail(typ, data)))
	}
	return out
}

func extDetail(typ uint16, d []byte) string {
	r := reader{b: d}
	switch typ {
	case 0: // server_name
		list := reader{b: r.vec16()}
		var names []string
		for list.left() > 0 && !list.bad {
			list.u8()
			names = append(names, string(list.vec16()))
		}
		return ": " + strings.Join(names, ", ")
	case 10: // supported_groups
		g := r.vec16()
		var names []string
		for i := 0; i+1 < len(g); i += 2 {
			id := binary.BigEndian.Uint16(g[i:])
			names = append(names, groupName(id))
		}
		return ": " + strings.Join(names, ", ")
	case 13: // signature_algorithms
		s := r.vec16()
		var names []string
		for i := 0; i+1 < len(s); i += 2 {
			names = append(names, fmt.Sprintf("0x%04x", binary.BigEndian.Uint16(s[i:])))
		}
		return ": " + strings.Join(names, " ")
	case 16: // ALPN
		list := reader{b: r.vec16()}
		var names []string
		for list.left() > 0 && !list.bad {
			names = append(names, string(list.vec8()))
		}
		return ": " + strings.Join(names, ", ")
	}
	if len(d) > 0 && len(d) <= 16 {
		return fmt.Sprintf(": % x", d)
	}
	return ""
}

// ---- names ----------------------------------------------------------------

func knownContentType(ct byte) bool { return ct >= 20 && ct <= 25 }

func contentTypeName(ct byte) string {
	switch ct {
	case 20:
		return "ChangeCipherSpec"
	case 21:
		return "Alert"
	case 22:
		return "Handshake"
	case 23:
		return "ApplicationData"
	case 24:
		return "Heartbeat"
	case 25:
		return "ConnectionID"
	}
	return fmt.Sprintf("type %d", ct)
}

func versionName(v uint16) string {
	switch v {
	case 0xfeff:
		return "1.0"
	case 0xfefd:
		return "1.2"
	case 0xfefc:
		return "1.3"
	}
	return fmt.Sprintf("0x%04x", v)
}

func handshakeName(t byte) string {
	names := map[byte]string{0: "HelloRequest", 1: "ClientHello", 2: "ServerHello", 3: "HelloVerifyRequest",
		4: "NewSessionTicket", 11: "Certificate", 12: "ServerKeyExchange", 13: "CertificateRequest",
		14: "ServerHelloDone", 15: "CertificateVerify", 16: "ClientKeyExchange", 20: "Finished"}
	if n, ok := names[t]; ok {
		return n
	}
	return fmt.Sprintf("handshake type %d", t)
}

func suiteName(id uint16) string {
	if n := dtls.CipherSuiteName(dtls.CipherSuiteID(id)); !strings.HasPrefix(n, "0x") {
		return n
	}
	if n := tls.CipherSuiteName(id); !strings.HasPrefix(n, "0x") {
		return n
	}
	switch id {
	case 0x00ff:
		return "TLS_EMPTY_RENEGOTIATION_INFO_SCSV"
	case 0x009f:
		return "TLS_DHE_RSA_WITH_AES_256_GCM_SHA384"
	case 0x009e:
		return "TLS_DHE_RSA_WITH_AES_128_GCM_SHA256"
	case 0xc0a3, 0xc0a2:
		return "TLS_DHE_RSA_WITH_AES_CCM_8"
	}
	return "(unknown)"
}

func extName(t uint16) string {
	names := map[uint16]string{0: "server_name", 1: "max_fragment_length", 5: "status_request", 10: "supported_groups",
		11: "ec_point_formats", 13: "signature_algorithms", 14: "use_srtp", 15: "heartbeat", 16: "ALPN",
		22: "encrypt_then_mac", 23: "extended_master_secret", 35: "session_ticket", 43: "supported_versions",
		51: "key_share", 54: "connection_id", 65281: "renegotiation_info"}
	if n, ok := names[t]; ok {
		return n
	}
	return "(unknown)"
}

func groupName(id uint16) string {
	switch id {
	case 23:
		return "secp256r1"
	case 24:
		return "secp384r1"
	case 25:
		return "secp521r1"
	case 29:
		return "x25519"
	case 30:
		return "x448"
	}
	return fmt.Sprintf("0x%04x", id)
}

func alertLevel(l byte) string {
	if l == 2 {
		return "fatal"
	}
	return "warning"
}

func alertName(d byte) string {
	names := map[byte]string{0: "close_notify", 10: "unexpected_message", 20: "bad_record_mac", 40: "handshake_failure",
		42: "bad_certificate", 43: "unsupported_certificate", 45: "certificate_expired", 46: "certificate_unknown",
		47: "illegal_parameter", 48: "unknown_ca", 50: "decode_error", 51: "decrypt_error", 70: "protocol_version",
		71: "insufficient_security", 80: "internal_error", 90: "user_canceled", 110: "unsupported_extension"}
	if n, ok := names[d]; ok {
		return n
	}
	return fmt.Sprintf("alert %d", d)
}

// ---- reader ---------------------------------------------------------------

// reader reads big-endian fields; past the end it sets bad and returns zeros.
type reader struct {
	b   []byte
	bad bool
}

func (r *reader) left() int { return len(r.b) }

func (r *reader) take(n int) []byte {
	if n > len(r.b) {
		r.bad = true
		r.b = nil
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *reader) skip(n int) { r.take(n) }

func (r *reader) u8() byte {
	if v := r.take(1); v != nil {
		return v[0]
	}
	return 0
}

func (r *reader) u16() uint16 {
	if v := r.take(2); v != nil {
		return binary.BigEndian.Uint16(v)
	}
	return 0
}

func (r *reader) vec8() []byte  { return r.take(int(r.u8())) }
func (r *reader) vec16() []byte { return r.take(int(r.u16())) }
