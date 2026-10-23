package main

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// A client carrier datagram from a real capture: the CONNECT_REQUEST, its
// retry (with the extra 01 byte) and a keep-alive ack.
const capturedConnect = "80 01 00 02 a0 00 05 03 00 00 ff ff 00 00 00 05 01 88 00 06 ff ff 00 00 00 05 01 01 88 00 02 ff ff 20 06"

func capturedBytes() []byte {
	b, _ := hex.DecodeString(strings.ReplaceAll(capturedConnect, " ", ""))
	return b
}

func TestParseCapturedDatagram(t *testing.T) {
	d, err := parseCarrierDatagram(capturedBytes())
	if err != nil {
		t.Fatal(err)
	}
	if d.Seq != 2 || len(d.Msgs) != 3 {
		t.Fatalf("seq %d, %d msgs", d.Seq, len(d.Msgs))
	}
	for i, want := range []string{"0000000501", "000000050101", "2006"} {
		if m := d.Msgs[i]; m.Channel != systemChannel || hex.EncodeToString(m.Data) != want {
			t.Errorf("msg %d: ch%d %x, want %s", i, m.Channel, m.Data, want)
		}
	}
	if onlyAcks(d) {
		t.Error("onlyAcks on a connect request")
	}
	checkRoundTrip(t, d)
	t.Log("\n" + describeCarrier(d))
}

func checkRoundTrip(t *testing.T, d carrierDatagram) {
	d2, err := parseCarrierDatagram(d.marshal())
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.Msgs) != len(d.Msgs) {
		t.Fatalf("%d msgs after round trip", len(d2.Msgs))
	}
	for i := range d.Msgs {
		a, b := d.Msgs[i], d2.Msgs[i]
		if a.Channel != b.Channel || a.Seq != b.Seq || a.RelSeq != b.RelSeq || !bytes.Equal(a.Data, b.Data) {
			t.Errorf("msg %d: %+v != %+v", i, a, b)
		}
	}
}

func TestSessionAnswersConnect(t *testing.T) {
	s := newCarrierSession()
	out, note := s.Receive(capturedBytes())
	if len(out) != 1 || !strings.Contains(note, "CONNECT_ACK") {
		t.Fatalf("%d datagrams, note %q", len(out), note)
	}
	d, err := parseCarrierDatagram(out[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + describeCarrier(d))
	if d.Prefix != [2]byte{0x80, 0x01} {
		t.Errorf("prefix %x", d.Prefix)
	}
	var ack, sync, connAck *carrierMsg
	for i := range d.Msgs {
		m := &d.Msgs[i]
		switch m.Data[len(m.Data)-1] {
		case smAcks:
			ack = m
		case smClockSync:
			sync = m
		case smConnectAck:
			connAck = m
		}
	}
	if ack == nil || sync == nil || connAck == nil {
		t.Fatal("missing ack, clock sync or connect ack")
	}
	if a, _ := parseAck(ack.Data[:len(ack.Data)-1]); !a.acks(2) {
		t.Error("datagram 2 not acked")
	}
	if !connAck.Reliable() || connAck.RelSeq != 0 || hex.EncodeToString(connAck.Data) != "0000000502" {
		t.Errorf("connect ack %+v", *connAck)
	}
	if connAck.Flags&mfConnecting != 0 {
		t.Error("connect ack marked connecting")
	}
	// Not acked yet: a tick re-sends it once it is overdue.
	if re := s.Tick(); len(re) != 0 && bytes.Contains(re[0], []byte{0, 0, 0, 5, smConnectAck}) {
		t.Fatal("re-sent at once")
	}
	for seq, p := range s.unacked {
		p.sent = p.sent.Add(-time.Second)
		s.unacked[seq] = p
	}
	re := s.Tick()
	if len(re) != 1 || !bytes.Contains(re[0], []byte{0, 0, 0, 5, smConnectAck}) {
		t.Fatalf("no re-send: %x", re)
	}
	// Ack the re-send; nothing more to re-send.
	rd, _ := parseCarrierDatagram(re[0])
	ackMsg := carrierDatagram{Prefix: d.Prefix, Seq: 3, Msgs: []carrierMsg{{Channel: systemChannel, Seq: 3, RelSeq: 0xffff, Data: append(ackData([]uint16{d.Seq, rd.Seq}), smAcks)}}}
	if !onlyAcks(ackMsg) {
		t.Error("onlyAcks on an ack")
	}
	s.Receive(ackMsg.marshal())
	if len(s.unacked) != 0 {
		t.Errorf("still unacked: %v", s.unacked)
	}
}

func TestAckData(t *testing.T) {
	for _, tc := range []struct {
		in   []uint16
		want []uint16
		not  []uint16
	}{
		{[]uint16{7}, []uint16{7}, []uint16{6, 8}},
		{[]uint16{5, 6, 7}, []uint16{5, 6, 7}, []uint16{4, 8}},
		{[]uint16{1, 3, 7}, []uint16{1, 3, 7}, []uint16{2, 4, 5, 6}},
		{[]uint16{0xfffe, 1}, []uint16{0xfffe, 1}, []uint16{0xffff, 0}},
	} {
		a, err := parseAck(ackData(tc.in))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range tc.want {
			if !a.acks(s) {
				t.Errorf("%v: %d not acked", tc.in, s)
			}
		}
		for _, s := range tc.not {
			if a.acks(s) {
				t.Errorf("%v: %d acked", tc.in, s)
			}
		}
	}
}
