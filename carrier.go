package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The world server ("REP") connection is GridMate Carrier, the transport of
// Amazon's Lumberyard engine, inside DTLS. This is an independent
// implementation of its wire format (big endian throughout):
//
//	datagram = prefix (2 bytes, New World's own; we echo the client's)
//	           sequence number (u16)
//	           messages...
//	message  = flags (u8) | data size (u16)
//	           [channel (u8)       if MF_DATA_CHANNEL, else the previous one, 0 at first]
//	           [chunks (u16)       if MF_CHUNKS]
//	           [sequence (u16)     unless MF_SEQUENTIAL_ID: then previous + 1]
//	           [reliable seq (u16) unless MF_SEQUENTIAL_REL_ID: then previous (+1 if reliable)]
//	           data
//
// Channel 3 carries the carrier's own system messages; their last data byte
// is the message id (SM_*). Reliable messages are delivered in order of their
// reliable sequence number, which starts at 0 (the last one before is 0xffff).

const (
	mfReliable    = 1 << 0
	mfChunks      = 1 << 2
	mfSeqID       = 1 << 3
	mfSeqRelID    = 1 << 4
	mfDataChannel = 1 << 5
	mfConnecting  = 1 << 7
	mfUnused      = 1<<1 | 1<<6

	carrierChannels = 4
	systemChannel   = 3

	smConnectRequest = 1
	smConnectAck     = 2
	smDisconnect     = 3
	smClockSync      = 4
	smAcks           = 6
	smConnControl    = 7
	smBandwidth      = 8

	ahfBits       = 1 << 7 // low 7 bits: number of ack bitmap bytes
	ahfContinuous = 1 << 6 // last, then first: everything in between is acked
	ahfKeepAlive  = 1 << 5 // no ack data
)

var systemMessageNames = map[byte]string{
	smConnectRequest: "SM_CONNECT_REQUEST", smConnectAck: "SM_CONNECT_ACK", smDisconnect: "SM_DISCONNECT",
	smClockSync: "SM_CLOCK_SYNC", smAcks: "SM_CT_ACKS", smConnControl: "SM_CT_CONN_CONTROL", smBandwidth: "SM_CT_BANDWIDTH",
}

type carrierMsg struct {
	Flags   byte
	Channel byte
	Chunks  uint16
	Seq     uint16
	RelSeq  uint16
	Data    []byte
}

func (m carrierMsg) Reliable() bool { return m.Flags&mfReliable != 0 }

type carrierDatagram struct {
	Prefix [2]byte
	Seq    uint16
	Msgs   []carrierMsg
}

var errCarrierShort = errors.New("carrier datagram truncated")

func parseCarrierDatagram(b []byte) (carrierDatagram, error) {
	var d carrierDatagram
	if len(b) < 4 {
		return d, errCarrierShort
	}
	copy(d.Prefix[:], b[:2])
	d.Seq = binary.BigEndian.Uint16(b[2:4])
	b = b[4:]
	var prevSeq, prevRel [carrierChannels]uint16
	channel := byte(0)
	u16 := func() (uint16, bool) {
		if len(b) < 2 {
			return 0, false
		}
		v := binary.BigEndian.Uint16(b)
		b = b[2:]
		return v, true
	}
	for len(b) > 0 {
		var m carrierMsg
		m.Flags = b[0]
		b = b[1:]
		if m.Flags&mfUnused != 0 {
			return d, fmt.Errorf("message flags 0x%02x use unused bits (misaligned stream?)", m.Flags)
		}
		size, ok := u16()
		if !ok {
			return d, errCarrierShort
		}
		if m.Flags&mfDataChannel != 0 {
			if len(b) < 1 {
				return d, errCarrierShort
			}
			channel = b[0]
			b = b[1:]
		}
		if channel >= carrierChannels {
			return d, fmt.Errorf("channel %d out of range", channel)
		}
		m.Channel = channel
		m.Chunks = 1
		if m.Flags&mfChunks != 0 {
			if m.Chunks, ok = u16(); !ok {
				return d, errCarrierShort
			}
		}
		if m.Flags&mfSeqID != 0 {
			prevSeq[channel]++
		} else if prevSeq[channel], ok = u16(); !ok {
			return d, errCarrierShort
		}
		m.Seq = prevSeq[channel]
		if m.Flags&mfSeqRelID != 0 {
			if m.Reliable() {
				prevRel[channel]++
			}
		} else if prevRel[channel], ok = u16(); !ok {
			return d, errCarrierShort
		}
		m.RelSeq = prevRel[channel]
		if int(size) > len(b) {
			return d, errCarrierShort
		}
		m.Data = b[:size]
		b = b[size:]
		d.Msgs = append(d.Msgs, m)
	}
	return d, nil
}

// marshal writes every header field out (no sequential shortcuts), which
// every reader accepts.
func (d carrierDatagram) marshal() []byte {
	out := append([]byte{}, d.Prefix[:]...)
	out = binary.BigEndian.AppendUint16(out, d.Seq)
	for _, m := range d.Msgs {
		flags := m.Flags&(mfReliable|mfConnecting) | mfDataChannel
		if m.Chunks > 1 {
			flags |= mfChunks
		}
		out = append(out, flags)
		out = binary.BigEndian.AppendUint16(out, uint16(len(m.Data)))
		out = append(out, m.Channel)
		if m.Chunks > 1 {
			out = binary.BigEndian.AppendUint16(out, m.Chunks)
		}
		out = binary.BigEndian.AppendUint16(out, m.Seq)
		out = binary.BigEndian.AppendUint16(out, m.RelSeq)
		out = append(out, m.Data...)
	}
	return out
}

// describeCarrier is the log text for one datagram.
func describeCarrier(d carrierDatagram) string {
	var b strings.Builder
	fmt.Fprintf(&b, "    carrier datagram: prefix %02x %02x, seq %d, %d message(s)\n", d.Prefix[0], d.Prefix[1], d.Seq, len(d.Msgs))
	for _, m := range d.Msgs {
		rel := "unreliable"
		if m.Reliable() {
			rel = "reliable"
		}
		conn := ""
		if m.Flags&mfConnecting != 0 {
			conn = ", connecting"
		}
		what := ""
		if m.Channel == systemChannel && len(m.Data) > 0 {
			id := m.Data[len(m.Data)-1]
			name := systemMessageNames[id]
			if name == "" {
				name = fmt.Sprintf("system message %d", id)
			}
			what = "  " + name
			if id == smAcks {
				what += " " + describeAck(m.Data[:len(m.Data)-1])
			}
		}
		chunks := ""
		if m.Chunks > 1 {
			chunks = fmt.Sprintf(", %d chunks", m.Chunks)
		}
		fmt.Fprintf(&b, "      ch%d seq %d rel %d %s%s%s, %d bytes%s: %s\n", m.Channel, m.Seq, m.RelSeq, rel, conn, chunks, len(m.Data), what, hex.EncodeToString(m.Data))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- acks --------------------------------------------------------------------

// ackSet is the result of reading SM_CT_ACKS data.
type ackSet struct {
	keepAlive   bool
	last, first uint16
	bits        []byte // bit i (byte i/8, bit i%8) acks last-1-i
}

func parseAck(b []byte) (ackSet, error) {
	var a ackSet
	if len(b) < 1 {
		return a, errCarrierShort
	}
	flags := b[0]
	b = b[1:]
	switch {
	case flags&ahfBits != 0:
		n := int(flags & 0x7f)
		if len(b) < 2+n {
			return a, errCarrierShort
		}
		a.last = binary.BigEndian.Uint16(b)
		a.first = a.last
		a.bits = b[2 : 2+n]
	case flags&ahfContinuous != 0:
		if len(b) < 4 {
			return a, errCarrierShort
		}
		a.last = binary.BigEndian.Uint16(b)
		a.first = binary.BigEndian.Uint16(b[2:])
	case flags&ahfKeepAlive != 0:
		a.keepAlive = true
	default:
		if len(b) < 2 {
			return a, errCarrierShort
		}
		a.last = binary.BigEndian.Uint16(b)
		a.first = a.last
	}
	return a, nil
}

func (a ackSet) acks(seq uint16) bool {
	if a.keepAlive {
		return false
	}
	if seq == a.last || seq == a.first {
		return true
	}
	if a.first != a.last && seqLess(a.first, seq) && seqLess(seq, a.last) {
		return true
	}
	d := int(uint16(a.last-seq)) - 1
	return d >= 0 && d/8 < len(a.bits) && a.bits[d/8]&(1<<(d%8)) != 0
}

func describeAck(b []byte) string {
	a, err := parseAck(b)
	switch {
	case err != nil:
		return "(bad: " + err.Error() + ")"
	case a.keepAlive:
		return "(keep-alive)"
	case a.bits != nil:
		return fmt.Sprintf("(last %d + %d bitmap bytes)", a.last, len(a.bits))
	case a.first != a.last:
		return fmt.Sprintf("(%d..%d)", a.first, a.last)
	}
	return fmt.Sprintf("(%d)", a.last)
}

// seqLess is a < b for wrapping 16-bit sequence numbers.
func seqLess(a, b uint16) bool { return int16(b-a) > 0 }

// ackData encodes acks for the received datagram numbers, like the client
// does: one number, a continuous range, or the last plus a bitmap.
func ackData(received []uint16) []byte {
	if len(received) == 0 {
		return []byte{ahfKeepAlive}
	}
	last, first := received[0], received[0]
	for _, s := range received {
		if seqLess(last, s) {
			last = s
		}
		if seqLess(s, first) {
			first = s
		}
	}
	if first == last {
		return binary.BigEndian.AppendUint16([]byte{0}, last)
	}
	if int(uint16(last-first))+1 == len(received) {
		out := binary.BigEndian.AppendUint16([]byte{ahfContinuous}, last)
		return binary.BigEndian.AppendUint16(out, first)
	}
	n := (int(uint16(last-first)) + 7) / 8
	if n > 64 {
		n = 64
	}
	bits := make([]byte, n)
	for _, s := range received {
		if d := int(uint16(last-s)) - 1; d >= 0 && d/8 < n {
			bits[d/8] |= 1 << (d % 8)
		}
	}
	out := binary.BigEndian.AppendUint16([]byte{ahfBits | byte(n)}, last)
	return append(out, bits...)
}

// ---- the session ---------------------------------------------------------------

// carrierSession is the server end of one client's carrier connection.
type carrierSession struct {
	mu        sync.Mutex
	start     time.Time
	prefix    [2]byte
	dgramSeq  uint16 // last datagram number sent
	sendSeq   [carrierChannels]uint16
	sendRel   [carrierChannels]uint16
	toAck     []uint16                // client datagrams received and not acked yet
	unacked   map[uint16]pendingDgram // our datagram -> reliable messages in it
	queue     []carrierMsg            // messages for the next datagram
	connected bool
	lastSent  time.Time
}

// pendingDgram is a sent datagram's reliable messages, kept until acked.
type pendingDgram struct {
	msgs []carrierMsg
	sent time.Time
}

const resendAfter = 250 * time.Millisecond

func newCarrierSession() *carrierSession {
	s := &carrierSession{start: time.Now(), unacked: map[uint16]pendingDgram{}}
	for i := range s.sendSeq {
		s.sendSeq[i], s.sendRel[i] = 0xffff, 0xffff
	}
	return s
}

// enqueue adds a message on a channel with the next sequence numbers.
func (s *carrierSession) enqueue(channel byte, reliable bool, data []byte) {
	s.sendSeq[channel]++
	m := carrierMsg{Channel: channel, Chunks: 1, Seq: s.sendSeq[channel], Data: data}
	if reliable {
		s.sendRel[channel]++
		m.Flags |= mfReliable
	}
	m.RelSeq = s.sendRel[channel]
	if !s.connected {
		m.Flags |= mfConnecting
	}
	s.queue = append(s.queue, m)
}

func (s *carrierSession) system(id byte, reliable bool, payload []byte) {
	s.enqueue(systemChannel, reliable, append(append([]byte{}, payload...), id))
}

// Receive handles one decrypted client datagram and returns the datagrams to
// send back, plus a log note.
func (s *carrierSession) Receive(b []byte) (out [][]byte, note string) {
	d, err := parseCarrierDatagram(b)
	if err != nil {
		return nil, "    carrier: cannot parse: " + err.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefix = d.Prefix
	s.toAck = append(s.toAck, d.Seq)
	var notes []string
	for _, m := range d.Msgs {
		if m.Channel != systemChannel || len(m.Data) == 0 {
			continue
		}
		id, payload := m.Data[len(m.Data)-1], m.Data[:len(m.Data)-1]
		switch id {
		case smAcks:
			if a, err := parseAck(payload); err == nil {
				for seq := range s.unacked {
					if a.acks(seq) {
						delete(s.unacked, seq)
					}
				}
			}
		case smConnectRequest:
			// The handshake payload is the client's version (u32); a Lumberyard
			// quirk appends one more id byte to it on every retry, so only the
			// first 4 bytes count. We answer with the same version.
			version := payload
			if len(version) > 4 {
				version = version[:4]
			}
			if !s.connected {
				s.connected = true
				s.system(smClockSync, false, binary.BigEndian.AppendUint32(nil, s.now()))
				s.system(smConnectAck, true, version)
				notes = append(notes, fmt.Sprintf("    carrier: CONNECT_REQUEST (version %x): answering CLOCK_SYNC + CONNECT_ACK", version))
			}
		case smDisconnect:
			notes = append(notes, fmt.Sprintf("    carrier: the client DISCONNECTED (reason data %x)", payload))
		}
	}
	return s.flush(true), strings.Join(notes, "\n")
}

// Tick re-sends unacked reliable messages and keeps the connection alive;
// call it every 100 ms or so.
func (s *carrierSession) Tick() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	// re-send the reliable messages unacked for too long, in order
	var seqs []uint16
	for seq, p := range s.unacked {
		if time.Since(p.sent) > resendAfter {
			seqs = append(seqs, seq)
		}
	}
	if len(seqs) > 0 {
		for i := 1; i < len(seqs); i++ {
			for j := i; j > 0 && seqLess(seqs[j], seqs[j-1]); j-- {
				seqs[j], seqs[j-1] = seqs[j-1], seqs[j]
			}
		}
		for _, seq := range seqs {
			s.queue = append(s.queue, s.unacked[seq].msgs...)
			delete(s.unacked, seq)
		}
		return s.flush(false)
	}
	if s.connected && time.Since(s.lastSent) > 100*time.Millisecond {
		return s.flush(true)
	}
	return nil
}

// flush builds one datagram: the queue, then acks (or a keep-alive).
func (s *carrierSession) flush(withAck bool) [][]byte {
	msgs := s.queue
	s.queue = nil
	if withAck {
		ack := ackData(s.toAck)
		s.toAck = nil
		s.sendSeq[systemChannel]++
		am := carrierMsg{Channel: systemChannel, Chunks: 1, Seq: s.sendSeq[systemChannel], RelSeq: s.sendRel[systemChannel], Data: append(ack, smAcks)}
		if !s.connected {
			am.Flags |= mfConnecting
		}
		msgs = append(msgs, am) // last, so sequence numbers stay in order
	}
	if len(msgs) == 0 {
		return nil
	}
	s.dgramSeq++
	var reliable []carrierMsg
	for _, m := range msgs {
		if m.Reliable() {
			reliable = append(reliable, m)
		}
	}
	if len(reliable) > 0 {
		s.unacked[s.dgramSeq] = pendingDgram{reliable, time.Now()}
	}
	s.lastSent = time.Now()
	return [][]byte{carrierDatagram{Prefix: s.prefix, Seq: s.dgramSeq, Msgs: msgs}.marshal()}
}

func (s *carrierSession) now() uint32 { return uint32(time.Since(s.start).Milliseconds()) }

// onlyAcks says whether a datagram carries nothing but SM_CT_ACKS.
func onlyAcks(d carrierDatagram) bool {
	for _, m := range d.Msgs {
		if m.Channel != systemChannel || len(m.Data) == 0 || m.Data[len(m.Data)-1] != smAcks {
			return false
		}
	}
	return len(d.Msgs) > 0
}
