package main

import (
	"testing"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// A guess at the shape of GetLoginInfoResult, with every kind of field the
// probe has to survive: lists of messages, nested messages, strings, numbers,
// bools, packed numbers, floats and a map.
const guessSchema = `
name: "guess.proto" package: "Javelin.RPC" syntax: "proto3"
message_type {
  name: "WorldMetrics"
  field { name: "worldPopulationStatus" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "load" number: 2 type: TYPE_FLOAT label: LABEL_OPTIONAL }
}
message_type {
  name: "World"
  field { name: "id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "name" number: 2 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "status" number: 3 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "metrics" number: 4 type: TYPE_MESSAGE type_name: ".Javelin.RPC.WorldMetrics" label: LABEL_OPTIONAL }
  field { name: "tags" number: 5 type: TYPE_INT32 label: LABEL_REPEATED }
  field { name: "weights" number: 6 type: TYPE_FLOAT label: LABEL_REPEATED }
  field { name: "full" number: 7 type: TYPE_BOOL label: LABEL_OPTIONAL }
  field { name: "attrs" number: 8 type: TYPE_MESSAGE type_name: ".Javelin.RPC.World.AttrsEntry" label: LABEL_REPEATED }
  field { name: "raw" number: 9 type: TYPE_BYTES label: LABEL_OPTIONAL }
  field { name: "ratio" number: 10 type: TYPE_DOUBLE label: LABEL_REPEATED }
  nested_type {
    name: "AttrsEntry" options { map_entry: true }
    field { name: "key" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL }
    field { name: "value" number: 2 type: TYPE_STRING label: LABEL_OPTIONAL }
  }
}
message_type {
  name: "Character"
  field { name: "character_id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "world_id" number: 3 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "name" number: 4 type: TYPE_STRING label: LABEL_OPTIONAL }
  field { name: "level" number: 5 type: TYPE_UINT32 label: LABEL_OPTIONAL }
  field { name: "far_away" number: 20 type: TYPE_STRING label: LABEL_OPTIONAL }
}
message_type {
  name: "GetLoginInfoResult"
  field { name: "count" number: 1 type: TYPE_INT32 label: LABEL_OPTIONAL }
  field { name: "worlds" number: 3 type: TYPE_MESSAGE type_name: ".Javelin.RPC.World" label: LABEL_REPEATED }
  field { name: "characters" number: 5 type: TYPE_MESSAGE type_name: ".Javelin.RPC.Character" label: LABEL_REPEATED }
  field { name: "recommended" number: 7 type: TYPE_MESSAGE type_name: ".Javelin.RPC.World" label: LABEL_OPTIONAL }
  field { name: "ids" number: 9 type: TYPE_INT64 label: LABEL_REPEATED }
  field { name: "flag" number: 17 type: TYPE_BOOL label: LABEL_OPTIONAL }
}
`

func guessResult(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	var fdp descriptorpb.FileDescriptorProto
	if err := prototext.Unmarshal([]byte(guessSchema), &fdp); err != nil {
		t.Fatal(err)
	}
	fd, err := protodesc.NewFile(&fdp, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fd.Messages().ByName("GetLoginInfoResult")
}

func TestProbeParsesAndLabelsEveryTextField(t *testing.T) {
	md := guessResult(t)
	msg := dynamicpb.NewMessage(md)
	// strict: proto3 string fields must hold valid UTF-8, as in the game
	if err := proto.Unmarshal(ProbeReply(), msg); err != nil {
		t.Fatalf("the probe does not parse as the guessed schema: %v", err)
	}
	get := func(m protoreflect.Message, path ...string) protoreflect.Value {
		var v protoreflect.Value
		for i, name := range path {
			fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
			v = m.Get(fd)
			if fd.IsList() {
				if v.List().Len() != 1 {
					t.Fatalf("%v: %d elements, want 1", path[:i+1], v.List().Len())
				}
				v = v.List().Get(0)
			}
			if i < len(path)-1 {
				m = v.Message()
			}
		}
		return v
	}
	for path, want := range map[string]string{
		"worlds.id":               "@3@1@_@_",
		"worlds.name":             "@3@2@_@_",
		"characters.name":         "@5@4@_@_",
		"characters.world_id":     "@5@3@_@_",
		"recommended.name":        "@7@2@_@_",
		"worlds.raw":              "@3@9@_@_",
		"characters.character_id": "@5@1@_@_",
	} {
		if got := get(msg, splitPath(path)...).Interface(); got != want && string(asBytes(got)) != want {
			t.Errorf("%s = %v, want %q", path, got, want)
		}
	}
	// the world's nested metrics message parses too (its fields are unknown or labels)
	if !get(msg, "worlds").Message().Has(md.Fields().ByName("worlds").Message().Fields().ByName("metrics")) {
		t.Error("worlds.metrics was not filled")
	}
	// a map entry parses (its key/value would be labels' bytes as fields)
	if get(msg, "worlds").Message().Get(md.Fields().ByName("worlds").Message().Fields().ByName("attrs")).Map().Len() != 1 {
		t.Error("worlds.attrs has no entry")
	}
}

func TestProbe2MatchesIDs(t *testing.T) {
	md := guessResult(t)
	msg := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(ProbeReply2(), msg); err != nil {
		t.Fatalf("probe 2 does not parse: %v", err)
	}
	world := msg.Get(md.Fields().ByName("worlds")).List().Get(0).Message()
	wf := md.Fields().ByName("worlds").Message().Fields()
	if got := world.Get(wf.ByName("id")).String(); got != "@W@W@W@W" {
		t.Errorf("world id = %q", got)
	}
	if got := world.Get(wf.ByName("name")).String(); got != "@W@W@W@W" {
		t.Errorf("world name = %q", got)
	}
}

func TestProbeIsASCIIBelowTheTop(t *testing.T) {
	for _, b := range [][]byte{ProbeReply(), ProbeReply2()} {
		checkASCIIBelowTop(t, b)
	}
}

func checkASCIIBelowTop(t *testing.T, b []byte) {
	t.Helper()
	for len(b) > 0 {
		_, typ, n := protowire.ConsumeTag(b)
		if n < 0 || typ != protowire.BytesType {
			t.Fatal("top level is not all length-delimited fields")
		}
		v, m := protowire.ConsumeBytes(b[n:])
		if m < 0 {
			t.Fatal("bad length")
		}
		for _, c := range v {
			if c >= 0x80 {
				t.Fatalf("inner message has a non-ASCII byte 0x%02x", c)
			}
		}
		b = b[n+m:]
	}
}

func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '.' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return out
}

func asBytes(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	return nil
}
