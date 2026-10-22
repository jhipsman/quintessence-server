package main

import (
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// gateway is a schema like the one we hope to find in the game.
func gateway() *descriptorpb.FileDescriptorProto {
	s, i32, msg := descriptorpb.FieldDescriptorProto_TYPE_STRING, descriptorpb.FieldDescriptorProto_TYPE_INT32, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	opt, rep := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL, descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("javelin/rpc/stubbed_gateway.proto"),
		Package: proto.String("Javelin.RPC"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Persona"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("name"), Number: proto.Int32(1), Type: &s, Label: &opt},
			}},
			{Name: proto.String("GetLoginInfoRequest"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("persona"), Number: proto.Int32(1), Type: &msg, Label: &opt, TypeName: proto.String(".Javelin.RPC.Persona")},
				{Name: proto.String("product_id"), Number: proto.Int32(2), Type: &s, Label: &opt},
				{Name: proto.String("version"), Number: proto.Int32(3), Type: &i32, Label: &opt},
				{Name: proto.String("tags"), Number: proto.Int32(4), Type: &msg, Label: &rep, TypeName: proto.String(".Javelin.RPC.GetLoginInfoRequest.TagsEntry")},
				{Name: proto.String("token"), Number: proto.Int32(5), Type: &s, Label: &opt, OneofIndex: proto.Int32(0)},
				{Name: proto.String("ticket"), Number: proto.Int32(6), Type: &s, Label: &opt, OneofIndex: proto.Int32(0)},
			},
				NestedType: []*descriptorpb.DescriptorProto{{
					Name:    proto.String("TagsEntry"),
					Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
					Field: []*descriptorpb.FieldDescriptorProto{
						{Name: proto.String("key"), Number: proto.Int32(1), Type: &s, Label: &opt},
						{Name: proto.String("value"), Number: proto.Int32(2), Type: &s, Label: &opt},
					},
				}},
				OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("auth")}},
			},
			{Name: proto.String("GetLoginInfoResponse")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("StubbedGatewayService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{Name: proto.String("GetLoginInfo"), InputType: proto.String(".Javelin.RPC.GetLoginInfoRequest"), OutputType: proto.String(".Javelin.RPC.GetLoginInfoResponse")},
				{Name: proto.String("Session"), InputType: proto.String(".Javelin.RPC.Persona"), OutputType: proto.String(".Javelin.RPC.Persona"), ClientStreaming: proto.Bool(true), ServerStreaming: proto.Bool(true)},
			},
		}},
	}
}

func TestExtractFromSyntheticBinary(t *testing.T) {
	raw, err := proto.Marshal(gateway())
	if err != nil {
		t.Fatal(err)
	}
	// embedded the way C++ does it: somewhere in .rdata, between other data
	bin := append([]byte("MZ\x90\x00junk .proto junk \x0a\x05x.proto"), make([]byte, 7)...)
	bin = append(bin, raw...)
	bin = append(bin, 0, 0, 0, 0)
	bin = append(bin, []byte("more data, mentioning javelin/rpc/stubbed_gateway.proto again")...)

	files := Extract(bin)
	if len(files) != 1 || files[0].GetName() != "javelin/rpc/stubbed_gateway.proto" {
		t.Fatalf("found %d: %v", len(files), files)
	}
	if !proto.Equal(files[0], gateway()) {
		t.Fatal("the extracted descriptor differs from the original")
	}

	text := PrintProto(files[0])
	for _, want := range []string{
		`syntax = "proto3";`,
		"package Javelin.RPC;",
		"message GetLoginInfoRequest {",
		".Javelin.RPC.Persona persona = 1;",
		"string product_id = 2;",
		"int32 version = 3;",
		"map<string, string> tags = 4;",
		"oneof auth {",
		"string ticket = 6;",
		"rpc GetLoginInfo(.Javelin.RPC.GetLoginInfoRequest) returns (.Javelin.RPC.GetLoginInfoResponse);",
		"rpc Session(stream .Javelin.RPC.Persona) returns (stream .Javelin.RPC.Persona);",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("printed proto lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "TagsEntry {") {
		t.Errorf("map entry printed as a message:\n%s", text)
	}
}

// The test binary itself is a Go program that links protobuf: its own
// descriptor.proto must be found, as it would be in any full-runtime program.
func TestExtractFromRealBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	var got *descriptorpb.FileDescriptorProto
	for _, f := range Extract(data) {
		if f.GetName() == "google/protobuf/descriptor.proto" {
			got = f
		}
	}
	if got == nil {
		t.Fatal("google/protobuf/descriptor.proto not found in the test binary")
	}
	if want := descriptorpb.File_google_protobuf_descriptor_proto.Messages().Len(); len(got.GetMessageType()) != want {
		t.Fatalf("%d messages, want %d", len(got.GetMessageType()), want)
	}
	if !strings.Contains(PrintProto(got), "message FileDescriptorProto {") {
		t.Fatal("printing lost FileDescriptorProto")
	}
}

func TestFindNames(t *testing.T) {
	bin := []byte("\x00/Javelin.RPC.StubbedGatewayService/GetLoginInfo\x00" +
		"Javelin.RPC.GetLoginInfoResponse\x00\x01" +
		"/grpc.health.v1.Health/Check\x00" +
		"Calling /Javelin.RPC.WorldService/Join failed\x00" +
		"/usr/lib/x86_64/libc.so\x00" +
		"GatewayLoginFailed: %s\x00")
	n := FindNames(bin)
	wantM := []string{"/Javelin.RPC.StubbedGatewayService/GetLoginInfo", "/Javelin.RPC.WorldService/Join", "/grpc.health.v1.Health/Check"}
	if strings.Join(n.Methods, ",") != strings.Join(wantM, ",") {
		t.Errorf("methods = %v", n.Methods)
	}
	if strings.Join(n.Types, ",") != "Javelin.RPC.GetLoginInfoResponse" {
		t.Errorf("types = %v (service names must not count as types)", n.Types)
	}
	if !strings.Contains(strings.Join(n.Related, "\n"), "GatewayLoginFailed: %s") {
		t.Errorf("related = %v", n.Related)
	}
}

func TestSafePath(t *testing.T) {
	for in, want := range map[string]string{
		"javelin/rpc/a.proto":   "javelin/rpc/a.proto",
		"../../evil.proto":      "evil.proto",
		`C:\x\y.proto`:          "x/y.proto",
		"/abs/./path//to.proto": "abs/path/to.proto",
	} {
		if got := safePath(in); got != want {
			t.Errorf("safePath(%q) = %q, want %q", in, got, want)
		}
	}
}
