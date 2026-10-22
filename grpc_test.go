package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// loginMsg is {1: "Alice", 2: 7, 3: {1: 42}} in protobuf wire format.
var loginMsg = []byte{0x0a, 0x05, 'A', 'l', 'i', 'c', 'e', 0x10, 0x07, 0x1a, 0x02, 0x08, 0x2a}

func startTCP(t *testing.T) (*TCPServer, string) {
	t.Helper()
	cap, base := newTestCapture(t)
	srv, err := ListenTCP("127.0.0.1:0", cap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	go srv.Serve()
	return srv, base
}

func dial(t *testing.T, srv *TCPServer) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///"+srv.ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodecV2(rawCodec{})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestGRPCUnaryCallIsLoggedAndAnswered(t *testing.T) {
	srv, base := startTCP(t)
	conn := dial(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-persona", "Alice", "trace-bin", "\x01\x02")
	req, resp := loginMsg, []byte(nil)
	err := conn.Invoke(ctx, "/javelin.gateway.Gateway/Login", &req, &resp)
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("the client should get Unimplemented, got %v", err)
	}

	log := waitFor(t, base, "answering Unimplemented")
	for _, want := range []string{
		"HTTP/2 preface, handing the connection to the gRPC server",
		"gRPC CALL  127.0.0.1:",
		"/javelin.gateway.Gateway/Login",
		"metadata x-persona: Alice",
		"metadata trace-bin: 0102",
		"deadline in ",
		"message 1, 13 bytes",
		`1: "Alice"`,
		"2: 7",
		"3: {",
		"  1: 42",
		"the client sent 1 message(s)",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q", want)
		}
	}
}

func TestGRPCCompressedCallsAreLogged(t *testing.T) {
	for _, comp := range []string{"gzip", "deflate"} {
		t.Run(comp, func(t *testing.T) {
			srv, base := startTCP(t)
			conn := dial(t, srv)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req, resp := loginMsg, []byte(nil)
			err := conn.Invoke(ctx, "/javelin.gateway.Gateway/Login", &req, &resp, grpc.UseCompressor(comp))
			if status.Code(err) != codes.Unimplemented {
				t.Fatalf("got %v", err)
			}
			log := waitFor(t, base, "answering Unimplemented")
			if !strings.Contains(log, `1: "Alice"`) {
				t.Errorf("log:\n%s", log)
			}
		})
	}
}

func TestGRPCEmptyReply(t *testing.T) {
	srv, base := startTCP(t)
	srv.grpc.replyEmpty = true
	conn := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, resp := loginMsg, []byte("not replaced")
	if err := conn.Invoke(ctx, "/Javelin.RPC.StubbedGatewayService/GetLoginInfo", &req, &resp); err != nil {
		t.Fatalf("the call should succeed: %v", err)
	}
	if len(resp) != 0 {
		t.Fatalf("reply = % x, want empty", resp)
	}
	waitFor(t, base, "answering with an EMPTY message and OK")
}

func TestGRPCStreamLogsEveryMessage(t *testing.T) {
	srv, base := startTCP(t)
	conn := dial(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	desc := &grpc.StreamDesc{StreamName: "Session", ClientStreams: true, ServerStreams: true}
	st, err := conn.NewStream(ctx, desc, "/javelin.gateway.Gateway/Session")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range [][]byte{loginMsg, {0x08, 0x01}} {
		if err := st.SendMsg(&m); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	var out []byte
	if err := st.RecvMsg(&out); status.Code(err) != codes.Unimplemented {
		t.Fatalf("stream end: %v", err)
	}
	log := waitFor(t, base, "the client sent 2 message(s)")
	if !strings.Contains(log, "message 2, 2 bytes") {
		t.Errorf("log:\n%s", log)
	}
}

func TestDumpProtobuf(t *testing.T) {
	got := strings.Join(DumpProtobuf(loginMsg), "\n")
	want := "1: \"Alice\"\n2: 7\n3: {\n  1: 42\n}"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// fixed-width fields and a negative int64
	got = strings.Join(DumpProtobuf([]byte{0x0d, 0, 0, 0x80, 0x3f, 0x10, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}), "\n")
	if !strings.Contains(got, "1: fixed32 1065353216 (float 1)") || !strings.Contains(got, "(int64 -1)") {
		t.Fatalf("got %s", got)
	}
	for _, bad := range [][]byte{{0x0a, 0x05, 'A'}, {0x00}, {0x0b}, {0xff}} {
		if DumpProtobuf(bad) != nil {
			t.Errorf("% x parsed as protobuf", bad)
		}
	}
}
