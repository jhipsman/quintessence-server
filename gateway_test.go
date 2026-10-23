package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// The login messages as recovered from the client's serializers
// (docs/PROTOCOL.md), to check the gateway's replies the strict way.
const loginSchema = `
name: "login.proto" package: "Javelin.RPC" syntax: "proto3"
message_type { name: "PersonaId" field { name: "id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL } }
message_type { name: "WorldMetrics"
  field { name: "worldAgeDays" number: 1 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "queueSize" number: 2 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "queueWaitTimeSec" number: 3 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "worldPopulationStatus" number: 4 type: TYPE_STRING label: LABEL_OPTIONAL } }
message_type { name: "WorldPublicMetadata"
  field { name: "id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "name" number: 2 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "status" number: 3 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "publicStatusCode" number: 4 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "version" number: 5 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "publicName" number: 6 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "worldType" number: 7 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "maxAccountCharacters" number: 8 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "worldSet" number: 9 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "metrics" number: 10 type: TYPE_MESSAGE type_name: ".Javelin.RPC.WorldMetrics" label: LABEL_OPTIONAL }
  field { name: "transferToRegion" number: 11 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "isFull" number: 12 type: TYPE_BOOL label: LABEL_OPTIONAL }
  field { name: "isRecommended" number: 13 type: TYPE_BOOL label: LABEL_OPTIONAL } }
message_type { name: "CharacterMetadata"
  field { name: "character_id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "persona_id" number: 2 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "world_id" number: 3 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "name" number: 4 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "published" number: 5 type: TYPE_BYTES label: LABEL_OPTIONAL }
  field { name: "status" number: 14 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "modified_date" number: 21 type: TYPE_STRING label: LABEL_OPTIONAL } }
message_type { name: "GetLoginInfoResult"
  field { name: "error" number: 1 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "worlds" number: 2 type: TYPE_MESSAGE type_name: ".Javelin.RPC.WorldPublicMetadata" label: LABEL_REPEATED }
  field { name: "characters" number: 3 type: TYPE_MESSAGE type_name: ".Javelin.RPC.CharacterMetadata" label: LABEL_REPEATED }
  field { name: "maxAccountCharacters" number: 5 type: TYPE_UINT32 label: LABEL_OPTIONAL }
  field { name: "maxChannelCharacters" number: 6 type: TYPE_UINT32 label: LABEL_OPTIONAL } }
message_type { name: "LoginToken"
  field { name: "rep_endpoint" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "world_id" number: 2 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "character_id" number: 3 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "persona_id" number: 4 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "signature" number: 8 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "ticket_id" number: 10 type: TYPE_STRING label: LABEL_OPTIONAL } }
message_type { name: "QueueLoginResult"
  field { name: "error" number: 1 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "ticket_id" number: 2 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "token" number: 4 type: TYPE_MESSAGE type_name: ".Javelin.RPC.LoginToken" label: LABEL_OPTIONAL } }
message_type { name: "CreateCharacterResult"
  field { name: "error" number: 1 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "character_id" number: 2 type: TYPE_STRING label: LABEL_OPTIONAL } }
`

func loginMessages(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	var fdp descriptorpb.FileDescriptorProto
	if err := prototext.Unmarshal([]byte(loginSchema), &fdp); err != nil {
		t.Fatal(err)
	}
	fd, err := protodesc.NewFile(&fdp, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func decode(t *testing.T, fd protoreflect.FileDescriptor, name string, b []byte) *dynamicpb.Message {
	t.Helper()
	m := dynamicpb.NewMessage(fd.Messages().ByName(protoreflect.Name(name)))
	if err := proto.Unmarshal(b, m); err != nil {
		t.Fatalf("%s does not parse: %v", name, err)
	}
	return m
}

func text(m *dynamicpb.Message) string { return prototext.MarshalOptions{}.Format(m) }

func personaReq(p string, rest ...[]byte) []byte {
	inner := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), p)
	b := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), inner)
	for _, r := range rest {
		b = append(b, r...)
	}
	return b
}

func TestGatewayLoginFlow(t *testing.T) {
	fd := loginMessages(t)
	path := filepath.Join(t.TempDir(), "characters.json")
	gw, err := NewGateway(path)
	if err != nil {
		t.Fatal(err)
	}
	const svc = "/Javelin.RPC.StubbedGatewayService/"

	// the request the game really sends (capture 2)
	login := []byte{0x0a, 0x08, 0x0a, 0x06, 'J', 'o', 'h', 'n', 'n', 'y', 0x12, 0x03, 'd', 'e', 'v', 0x18, 0x01}
	out, _, ok := gw.Handle(svc+"GetLoginInfo", [][]byte{login}, "127.0.0.1:6000")
	if !ok {
		t.Fatal("GetLoginInfo not handled")
	}
	info := text(decode(t, fd, "GetLoginInfoResult", out))
	for _, want := range []string{`id:"quintessence-1"`, `version:"1.400.6031.6004151"`, `worldType:"OpenWorld"`, `status:"ACTIVE"`, `maxAccountCharacters:4`, `maxChannelCharacters:4`} {
		if !strings.Contains(strings.ReplaceAll(info, " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("GetLoginInfoResult lacks %s:\n%s", want, info)
		}
	}
	if strings.Contains(info, "characters") {
		t.Errorf("a new persona has characters:\n%s", info)
	}

	// CreateCharacterRequest: 1 PersonaId, 2 {1 name, 2 appearance}, 3 world_id
	params := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "Aldric")
	params = protowire.AppendBytes(protowire.AppendTag(params, 2, protowire.BytesType), []byte{0x08, 0x05})
	create := personaReq("Johnny",
		protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), params),
		protowire.AppendString(protowire.AppendTag(nil, 3, protowire.BytesType), "quintessence-1"))
	out, _, _ = gw.Handle(svc+"CreateCharacter", [][]byte{create}, "")
	cr := decode(t, fd, "CreateCharacterResult", out)
	charID := cr.Get(cr.Descriptor().Fields().ByName("character_id")).String()
	if charID == "" {
		t.Fatalf("no character id: %s", text(cr))
	}

	// it is listed for its persona, and survives a restart
	gw2, err := NewGateway(path)
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ = gw2.Handle(svc+"GetLoginInfo", [][]byte{login}, "")
	info = text(decode(t, fd, "GetLoginInfoResult", out))
	for _, want := range []string{charID, `name:"Aldric"`, `world_id:"quintessence-1"`, `persona_id:"Johnny"`, `published:"\x08\x05"`} {
		if !strings.Contains(info, want) {
			t.Errorf("after create, GetLoginInfoResult lacks %s:\n%s", want, info)
		}
	}
	// not for another persona
	other := personaReq("Someone")
	out, _, _ = gw2.Handle(svc+"GetLoginInfo", [][]byte{other}, "")
	if strings.Contains(text(decode(t, fd, "GetLoginInfoResult", out)), "Aldric") {
		t.Error("another persona sees the character")
	}

	// QueueLoginRequest as the game sends it: 1 PersonaId, 2 character_id, no world
	q := personaReq("Johnny", protowire.AppendString(protowire.AppendTag(nil, 2, protowire.BytesType), charID))
	out, _, _ = gw2.Handle(svc+"QueueLogin", [][]byte{q}, "127.0.0.1:6000")
	qr := decode(t, fd, "QueueLoginResult", out)
	ql := text(qr)
	for _, want := range []string{`rep_endpoint:"127.0.0.1:6000"`, `world_id:"quintessence-1"`, `character_id:"` + charID, `persona_id:"Johnny"`, `ticket_id:"ticket-`, `signature:"`} {
		if !strings.Contains(strings.ReplaceAll(ql, " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("QueueLoginResult lacks %s:\n%s", want, ql)
		}
	}
	// RefreshQueuedLogin (1 ticket_id) gets the very same ticket and token
	ticket := qr.Get(qr.Descriptor().Fields().ByName("ticket_id")).String()
	refresh := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), ticket)
	out2, _, _ := gw2.Handle(svc+"RefreshQueuedLogin", [][]byte{refresh}, "127.0.0.1:6000")
	if string(out2) != string(out) {
		t.Errorf("refresh changed the reply:\n%s\nvs\n%s", text(decode(t, fd, "QueueLoginResult", out2)), ql)
	}
	unknown := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "ticket-nope")
	out3, _, _ := gw2.Handle(svc+"RefreshQueuedLogin", [][]byte{unknown}, "")
	if e := decode(t, fd, "QueueLoginResult", out3); e.Get(e.Descriptor().Fields().ByName("error")).Int() == 0 {
		t.Error("an unknown ticket must get an error code")
	}

	// delete
	del := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), charID)
	gw2.Handle(svc+"DeactivateCharacter", [][]byte{del}, "")
	out, _, _ = gw2.Handle(svc+"GetLoginInfo", [][]byte{login}, "")
	if strings.Contains(text(decode(t, fd, "GetLoginInfoResult", out)), "Aldric") {
		t.Error("the deleted character is still listed")
	}

	// calls outside the login flow are left to the caller
	if _, _, ok := gw2.Handle("/Javelin.RPC.StubbedFcsService/GetCatalog", nil, ""); ok {
		t.Error("GetCatalog should not be handled")
	}
}

func TestGRPCGatewayEndToEnd(t *testing.T) {
	srv, base := startTCP(t)
	gw, err := NewGateway(filepath.Join(t.TempDir(), "characters.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv.grpc.reply, srv.grpc.gw = "gateway", gw
	conn := dial(t, srv)
	ctx, cancel := contextWithTimeout()
	defer cancel()
	req := []byte{0x0a, 0x08, 0x0a, 0x06, 'J', 'o', 'h', 'n', 'n', 'y', 0x12, 0x03, 'd', 'e', 'v', 0x18, 0x01}
	var resp []byte
	if err := conn.Invoke(ctx, "/Javelin.RPC.StubbedGatewayService/GetLoginInfo", &req, &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp), clientVersion) {
		t.Fatalf("reply has no world: % x", resp)
	}
	waitFor(t, base, "GATEWAY answer")
}

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
