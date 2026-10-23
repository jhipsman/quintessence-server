package main

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/rep-login-datagram.hex is the client's first datagram after the
// carrier connect, decrypted: LZ4-compressed, an ack plus the registration
// request on channel 0.
func loginDatagram(t *testing.T) []byte {
	h, err := os.ReadFile("testdata/rep-login-datagram.hex")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(h)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVarint(t *testing.T) {
	for _, v := range []uint32{0, 1, 0x7f, 0x80, 0x3fff, 0x4000, 0x1fffff, 0x200000, 0xfffffff, 0x10000000, 0xffffffff} {
		b := appendVarint(nil, v)
		got, n, ok := readVarint(b)
		if !ok || got != v || n != len(b) {
			t.Errorf("%#x: %x -> %#x, %d, %v", v, b, got, n, ok)
		}
	}
}

func TestCompressedLoginDatagram(t *testing.T) {
	d, err := parseCarrierDatagram(loginDatagram(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + describeCarrier(d))
	var req *carrierMsg
	for i := range d.Msgs {
		if d.Msgs[i].Channel == 0 {
			req = &d.Msgs[i]
		}
	}
	if d.Compressed == 0 || req == nil || !req.Reliable() || req.RelSeq != 0 {
		t.Fatalf("compressed %d, request %+v", d.Compressed, req)
	}
	payloads, err := splitRepFrames(req.Data)
	if err != nil || len(payloads) != 1 {
		t.Fatalf("%d frames, %v", len(payloads), err)
	}
	m, err := parseRepPayload(payloads[0])
	if err != nil {
		t.Fatal(err)
	}
	if m.Target != [16]byte{} || m.TypeIndex != 2 || !strings.Contains(m.TypeName(), "RegistrationRequestV3Msg") {
		t.Errorf("message %+v", m)
	}
	strs := strings.Join(printableStrings(m.Body), " ")
	for _, want := range []string{"Javelin", "6004151", "[RETAIL]", "127.0.0.1:6000", "quintessence-1", "Johnny", "rpc-channel"} {
		if !strings.Contains(strs, want) {
			t.Errorf("no %q in %s", want, strs)
		}
	}
}

func TestSessionAuthorizesLogin(t *testing.T) {
	s := newCarrierSession()
	s.Receive(capturedBytes()) // the connect
	out, note := s.Receive(loginDatagram(t))
	t.Log(note)
	if !strings.Contains(note, "authorized") || len(out) != 1 {
		t.Fatalf("%d datagrams, note %q", len(out), note)
	}
	d, err := parseCarrierDatagram(out[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + describeCarrier(d))
	if d.Prefix != [2]byte{0x80, 0x01} {
		t.Errorf("prefix %x: we must not claim compression", d.Prefix)
	}
	var resp *carrierMsg
	for i := range d.Msgs {
		if d.Msgs[i].Channel == 0 {
			resp = &d.Msgs[i]
		}
	}
	if resp == nil || !resp.Reliable() || resp.RelSeq != 0 {
		t.Fatalf("response %+v", resp)
	}
	payloads, err := splitRepFrames(resp.Data)
	if err != nil || len(payloads) != 1 {
		t.Fatalf("%d frames, %v", len(payloads), err)
	}
	m, err := parseRepPayload(payloads[0])
	if err != nil {
		t.Fatal(err)
	}
	if m.TypeIndex != 0 || m.Type != typeRegistrationResponse {
		t.Errorf("type %d %x", m.TypeIndex, m.Type)
	}
	if !bytes.Equal(m.Body[:4], []byte{0, 0, 0, 0}) || !bytes.HasSuffix(m.Body, []byte{0, 0, 0, 0}) {
		t.Errorf("body %x", m.Body)
	}
	// A re-send of the same request is acked but not answered again.
	if _, note := s.Receive(loginDatagram(t)); strings.Contains(note, "authorized") {
		t.Error("answered a re-send")
	}
}

func TestRegistrationResponseLayout(t *testing.T) {
	p := registrationResponse(1500*time.Millisecond, "ab", "v1")
	want := "00000000000000000000000000000000" + "000100" + hex.EncodeToString(typeRegistrationResponse[:]) +
		"00000000" + "0000000059682f00" + "026162" + "027631" + "00000000"
	if got := hex.EncodeToString(p); got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}
