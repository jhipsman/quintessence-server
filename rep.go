package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"time"
)

// Above the carrier, the world server ("REP", Amazon's Manifold) speaks in
// frames carried by reliable messages on carrier channel 0. The two
// directions are framed differently:
//
//	client -> REP: crc32 of the payload (u32) | payload length (u32) | payload
//	               payload = target actor id (16 bytes, zero for the REP itself)
//	                         MessageEnvelope
//	               (client marshaller vtable 0x148590be8)
//	REP -> client: a byte stream of varint length | MessageEnvelope
//	               (client parser 0x146ae44f0; no CRC, no actor id)
//
//	envelope = flags (u8: 1 -> 8 more bytes, 2 -> 8 more bytes, 4 -> routing block)
//	           message present (u8, 0 or 1)
//	           type: varint index into the client's type table, or 0 and the
//	                 type's 16-byte UUID
//	           message body
//
// Integers are big endian. Strings and counts use a varint whose first byte
// says its length: 0xxxxxxx, 10xxxxxx +1 byte, 110xxxxx +2, 1110xxxx +3,
// 11110xxx +4 (low bits first). See docs/PROTOCOL.md.

var (
	// RegistrationResponseMsg: u32 result (0 = authorized), u64 server clock
	// (ns), string (kept by the client), string (the server version), and four
	// flags; the fourth asks the client to start Easy Anti-Cheat.
	typeRegistrationResponse = mustUUID("104145a7-ff95-44f1-9468-21fb41c8ac2b")
)

// repTypeNames names the type indexes and UUIDs seen so far.
var repTypeNames = map[string]string{
	"index 2": "RegistrationRequestV3Msg",
	hex.EncodeToString(typeRegistrationResponse[:]): "RegistrationResponseMsg",
}

func mustUUID(s string) (u [16]byte) {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		panic("bad uuid " + s)
	}
	copy(u[:], b)
	return u
}

// ---- varints and strings ---------------------------------------------------

func appendVarint(b []byte, v uint32) []byte {
	switch {
	case v < 0x80:
		return append(b, byte(v))
	case v < 0x4000:
		return append(b, 0x80|byte(v&0x3f), byte(v>>6))
	case v < 0x200000:
		return append(b, 0xc0|byte(v&0x1f), byte(v>>5), byte(v>>13))
	case v < 0x10000000:
		return append(b, 0xe0|byte(v&0x0f), byte(v>>4), byte(v>>12), byte(v>>20))
	}
	return append(b, 0xf0|byte(v&0x07), byte(v>>3), byte(v>>11), byte(v>>19), byte(v>>27))
}

func readVarint(b []byte) (v uint32, n int, ok bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	c := b[0]
	var extra int
	var low uint32
	switch {
	case c < 0x80:
		return uint32(c), 1, true
	case c < 0xc0:
		extra, low = 1, uint32(c&0x3f)
	case c < 0xe0:
		extra, low = 2, uint32(c&0x1f)
	case c < 0xf0:
		extra, low = 3, uint32(c&0x0f)
	default:
		extra, low = 4, uint32(c&0x07)
	}
	if len(b) < 1+extra {
		return 0, 0, false
	}
	shift := uint(7 - extra) // bits in the first byte
	v = low
	for i := 0; i < extra; i++ {
		v |= uint32(b[1+i]) << (shift + 8*uint(i))
	}
	return v, 1 + extra, true
}

func appendRepString(b []byte, s string) []byte {
	return append(appendVarint(b, uint32(len(s))), s...)
}

// ---- frames ----------------------------------------------------------------

var errRepShort = errors.New("REP frame truncated")

// splitRepFrames splits a channel-0 message into frames and checks their CRCs.
func splitRepFrames(b []byte) (payloads [][]byte, err error) {
	for len(b) > 0 {
		if len(b) < 8 {
			return payloads, errRepShort
		}
		sum, n := binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:])
		if uint64(n) > uint64(len(b)-8) {
			return payloads, errRepShort
		}
		p := b[8 : 8+n]
		if crc32.ChecksumIEEE(p) != sum {
			return payloads, fmt.Errorf("REP frame CRC %08x, computed %08x", sum, crc32.ChecksumIEEE(p))
		}
		payloads = append(payloads, p)
		b = b[8+n:]
	}
	return payloads, nil
}

// repUpFrame frames a client -> REP payload (used by tests).
func repUpFrame(payload []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(payload))
	out = binary.BigEndian.AppendUint32(out, uint32(len(payload)))
	return append(out, payload...)
}

// repDownFrame frames a REP -> client envelope.
func repDownFrame(envelope []byte) []byte {
	return append(appendVarint(nil, uint32(len(envelope))), envelope...)
}

// splitRepDownFrames splits a REP -> client stream into envelopes.
func splitRepDownFrames(b []byte) (envelopes [][]byte, err error) {
	for len(b) > 0 {
		n, k, ok := readVarint(b)
		if !ok || uint64(n) > uint64(len(b)-k) {
			return envelopes, errRepShort
		}
		envelopes = append(envelopes, b[k:k+int(n)])
		b = b[k+int(n):]
	}
	return envelopes, nil
}

// repMessage is one decoded payload.
type repMessage struct {
	Target    [16]byte
	Flags     byte
	TypeIndex uint32   // 0: Type holds the UUID
	Type      [16]byte // when TypeIndex is 0
	Body      []byte
}

func (m repMessage) TypeName() string {
	key := fmt.Sprintf("index %d", m.TypeIndex)
	if m.TypeIndex == 0 {
		key = hex.EncodeToString(m.Type[:])
	}
	if n := repTypeNames[key]; n != "" {
		return n + " (" + key + ")"
	}
	return "unknown type " + key
}

// parseRepPayload reads a client -> REP payload: actor id, then envelope.
func parseRepPayload(p []byte) (m repMessage, err error) {
	if len(p) < 16 {
		return m, errRepShort
	}
	m, err = parseRepEnvelope(p[16:])
	copy(m.Target[:], p)
	return m, err
}

func parseRepEnvelope(p []byte) (m repMessage, err error) {
	if len(p) < 2 {
		return m, errRepShort
	}
	m.Flags = p[0]
	p = p[1:]
	if m.Flags&^1&^2 != 0 {
		return m, fmt.Errorf("envelope flags 0x%02x (routing block) not handled", m.Flags)
	}
	for _, bit := range []byte{1, 2} {
		if m.Flags&bit != 0 {
			if len(p) < 8 {
				return m, errRepShort
			}
			p = p[8:]
		}
	}
	if len(p) < 1 || p[0] != 1 {
		return m, errors.New("envelope without a message")
	}
	idx, n, ok := readVarint(p[1:])
	if !ok {
		return m, errRepShort
	}
	m.TypeIndex = idx
	p = p[1+n:]
	if idx == 0 {
		if len(p) < 16 {
			return m, errRepShort
		}
		copy(m.Type[:], p)
		p = p[16:]
	}
	m.Body = p
	return m, nil
}

// repEnvelope builds an envelope that names its type by UUID.
func repEnvelope(typ [16]byte, body []byte) []byte {
	p := make([]byte, 0, 3+16+len(body))
	p = append(p, 0, 1, 0) // no envelope flags, a message, type by UUID
	p = append(p, typ[:]...)
	return append(p, body...)
}

// registrationResponse authorizes the client.
func registrationResponse(clock time.Duration, sessionID, serverVersion string) []byte {
	b := binary.BigEndian.AppendUint32(nil, 0) // authorized
	b = binary.BigEndian.AppendUint64(b, uint64(clock.Nanoseconds()))
	b = appendRepString(b, sessionID)
	b = appendRepString(b, serverVersion)
	b = append(b, 0, 0, 0, 0) // no Easy Anti-Cheat
	return repEnvelope(typeRegistrationResponse, b)
}

// printableStrings lists the length-prefixed strings in a body, for the log.
func printableStrings(b []byte) []string {
	var out []string
	for i := 0; i < len(b); i++ {
		n := int(b[i])
		if n < 3 || i+1+n > len(b) {
			continue
		}
		s := b[i+1 : i+1+n]
		good := true
		for _, c := range s {
			if c < 0x20 || c > 0x7e {
				good = false
				break
			}
		}
		if good {
			out = append(out, string(s))
			i += n
		}
	}
	return out
}

// repSession is the REP end of one client.
type repSession struct {
	start time.Time
}

func newRepSession() *repSession { return &repSession{start: time.Now()} }

// Handle takes one reliable channel-0 message and returns the frames to send
// back (as channel-0 reliable messages), plus log notes.
func (r *repSession) Handle(data []byte) (replies [][]byte, notes []string) {
	payloads, err := splitRepFrames(data)
	if err != nil {
		notes = append(notes, "    REP: "+err.Error())
	}
	for _, p := range payloads {
		m, err := parseRepPayload(p)
		if err != nil {
			notes = append(notes, fmt.Sprintf("    REP: cannot read the envelope (%v): %x", err, p))
			continue
		}
		notes = append(notes, fmt.Sprintf("    REP message to actor %x: %s, %d-byte body\n      strings: %q", m.Target, m.TypeName(), len(m.Body), printableStrings(m.Body)))
		if m.TypeIndex == 2 {
			replies = append(replies, repDownFrame(registrationResponse(time.Since(r.start), "quintessence-session", "Quintessence 1.400.6031.6004151")))
			notes = append(notes, "    REP: answering RegistrationResponseMsg: authorized")
		}
	}
	return replies, notes
}

// ---- LZ4 -------------------------------------------------------------------

// lz4Block decompresses an LZ4 block (no frame header), as carrier datagrams
// with prefix bit 0x01 carry their body.
func lz4Block(src []byte) ([]byte, error) {
	out := make([]byte, 0, len(src)*3)
	for i := 0; i < len(src); {
		token := src[i]
		i++
		n := int(token >> 4)
		if n == 15 {
			for {
				if i >= len(src) {
					return nil, errors.New("lz4: truncated length")
				}
				c := src[i]
				i++
				n += int(c)
				if c != 255 {
					break
				}
			}
		}
		if i+n > len(src) {
			return nil, errors.New("lz4: truncated literals")
		}
		out = append(out, src[i:i+n]...)
		i += n
		if i >= len(src) {
			break // the last sequence has literals only
		}
		if i+2 > len(src) {
			return nil, errors.New("lz4: truncated offset")
		}
		off := int(src[i]) | int(src[i+1])<<8
		i += 2
		if off == 0 || off > len(out) {
			return nil, fmt.Errorf("lz4: bad offset %d", off)
		}
		m := int(token & 15)
		if m == 15 {
			for {
				if i >= len(src) {
					return nil, errors.New("lz4: truncated match length")
				}
				c := src[i]
				i++
				m += int(c)
				if c != 255 {
					break
				}
			}
		}
		m += 4
		for k := 0; k < m; k++ {
			out = append(out, out[len(out)-off])
		}
	}
	return out, nil
}
