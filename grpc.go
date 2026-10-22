package main

import (
	"bytes"
	"compress/flate"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/gzip" // the client may compress its messages
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// The game's gateway client is C++ gRPC over cleartext HTTP/2 (h2c, prior
// knowledge): it opens a TCP connection with the HTTP/2 preface. This server
// takes every call, whatever its method, logs the method, the metadata and
// each message, and answers Unimplemented once the client has sent all its
// messages. That way the client gets an answer and goes on to its next call,
// and each run shows us one more step of the protocol.

// GRPCServer serves the TCP connections that open with the HTTP/2 preface.
type GRPCServer struct {
	srv *grpc.Server
	ln  *chanListener
	cap *Capture

	// reply is how a call that the client has finished sending is answered:
	//   "unimplemented"  status Unimplemented
	//   "empty"          one empty message and OK: valid for any message type
	//                    (every field at its default), so the client takes it
	//                    as a success and shows us what it does next
	//   "probe"          GetLoginInfo gets the probe message (probe.go), every
	//                    other call an empty message
	//   "probe2", "probe3"  the same with probe 2 or 3
	//   "gateway"        real answers for the login flow (gateway.go), an
	//                    empty message for every other call
	reply string
	gw    *Gateway
}

func NewGRPCServer(cap *Capture, local net.Addr) *GRPCServer {
	g := &GRPCServer{ln: newChanListener(local), cap: cap}
	g.srv = grpc.NewServer(
		grpc.ForceServerCodecV2(rawCodec{}),
		grpc.UnknownServiceHandler(g.handle),
	)
	return g
}

func (g *GRPCServer) Serve()         { _ = g.srv.Serve(g.ln) }
func (g *GRPCServer) Close()         { g.srv.Stop() }
func (g *GRPCServer) Add(c net.Conn) { g.ln.push(c) }

func (g *GRPCServer) handle(_ any, stream grpc.ServerStream) (err error) {
	defer func() {
		if r := recover(); r != nil {
			g.cap.Notef("PANIC in the gRPC handler (the server keeps running): %v\n%s", r, bytes.TrimSpace(debug.Stack()))
			err = status.Error(codes.Internal, "quintessence stub: internal error")
		}
	}()
	ctx := stream.Context()
	method, _ := grpc.MethodFromServerStream(stream)
	client := "?"
	if p, ok := peer.FromContext(ctx); ok {
		ap := addrPort(p.Addr)
		client = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "gRPC CALL  %s  %s\n", client, method)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		keys := make([]string, 0, len(md))
		for k := range md {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range md[k] {
				if strings.HasSuffix(k, "-bin") {
					v = hex.EncodeToString([]byte(v))
				}
				fmt.Fprintf(&b, "    metadata %s: %s\n", k, v)
			}
		}
	}
	if dl, ok := ctx.Deadline(); ok {
		fmt.Fprintf(&b, "    deadline in %s\n", time.Until(dl).Round(time.Millisecond))
	}
	g.cap.Notef("%s", strings.TrimRight(b.String(), "\n"))
	// the address the client dialed, which the login token sends it back to
	endpoint := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok && len(md[":authority"]) > 0 {
		endpoint = md[":authority"][0]
	}

	var msgs [][]byte
	for i := 1; ; i++ {
		var msg []byte
		err := stream.RecvMsg(&msg)
		if errors.Is(err, io.EOF) {
			if g.reply == "gateway" && g.gw != nil {
				if out, note, ok := g.gw.Handle(method, msgs, endpoint); ok {
					if out == nil {
						out = []byte{}
					}
					g.cap.Notef("gRPC END   %s  %s: the client sent %d message(s); GATEWAY answer (%d bytes): %s\n%s",
						client, method, i-1, len(out), note, strings.TrimRight(describeMessage(client, method+" (reply)", 1, out), "\n"))
					if err := stream.SendMsg(&out); err != nil {
						g.cap.Notef("gRPC END   %s  %s: sending the reply failed: %v", client, method, err)
						return err
					}
					return nil
				}
			}
			if strings.HasPrefix(g.reply, "probe") && strings.HasSuffix(method, "/GetLoginInfo") {
				out, note := ProbeReply(), "In the game, a text like @3@2@_@_ means: top-level field 3, then field 2 (fields 10-20 are a-k)."
				switch g.reply {
				case "probe2":
					out, note = ProbeReply2(), "Every text is @W@W@W@W except character names (field 3 -> 4), which are @N@a@m@e."
				case "probe3":
					out, note = ProbeReply3(), "14 characters @C@j: only field j is @W@W@W@W; the one missing from the list has world_id in field j. @E@m@p@t has every field empty."
				}
				g.cap.Notef("gRPC END   %s  %s: the client sent %d message(s); answering with the %s message (%d bytes) and OK.\n    %s",
					client, method, i-1, strings.ToUpper(g.reply), len(out), note)
				if err := stream.SendMsg(&out); err != nil {
					g.cap.Notef("gRPC END   %s  %s: sending the reply failed: %v", client, method, err)
					return err
				}
				return nil
			}
			if g.reply == "empty" || g.reply == "gateway" || strings.HasPrefix(g.reply, "probe") {
				g.cap.Notef("gRPC END   %s  %s: the client sent %d message(s); answering with an EMPTY message and OK", client, method, i-1)
				empty := []byte{}
				if err := stream.SendMsg(&empty); err != nil {
					g.cap.Notef("gRPC END   %s  %s: sending the reply failed: %v", client, method, err)
					return err
				}
				return nil
			}
			g.cap.Notef("gRPC END   %s  %s: the client sent %d message(s); answering Unimplemented", client, method, i-1)
			return status.Errorf(codes.Unimplemented, "quintessence stub: %s is not implemented yet", method)
		}
		if err != nil {
			g.cap.Notef("gRPC END   %s  %s: %v", client, method, err)
			return err
		}
		g.cap.Notef("%s", describeMessage(client, method, i, msg))
		msgs = append(msgs, msg)
	}
}

func describeMessage(client, method string, n int, msg []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "gRPC MSG   %s  %s  message %d, %d bytes\n", client, method, n, len(msg))
	if fields := DumpProtobuf(msg); fields != nil {
		b.WriteString("    protobuf (field numbers, no schema):\n")
		for _, f := range fields {
			b.WriteString("      " + f + "\n")
		}
	} else if len(msg) > 0 {
		b.WriteString("    not a valid protobuf message\n")
	}
	b.WriteString(indent(hex.Dump(msg)))
	return strings.TrimRight(b.String(), "\n")
}

// deflate is the other compression C++ gRPC offers (grpc-go only ships gzip).
type deflate struct{}

func init() { encoding.RegisterCompressor(deflate{}) }

func (deflate) Name() string { return "deflate" }

func (deflate) Compress(w io.Writer) (io.WriteCloser, error) {
	return flate.NewWriter(w, flate.DefaultCompression)
}

func (deflate) Decompress(r io.Reader) (io.Reader, error) { return flate.NewReader(r), nil }

// rawCodec hands the messages over as bytes: we have no .proto files.
type rawCodec struct{}

func (rawCodec) Name() string { return "proto" }

func (rawCodec) Marshal(v any) (mem.BufferSlice, error) {
	b, ok := v.(*[]byte)
	if !ok {
		return nil, fmt.Errorf("rawCodec: cannot marshal %T", v)
	}
	return mem.BufferSlice{mem.SliceBuffer(*b)}, nil
}

func (rawCodec) Unmarshal(data mem.BufferSlice, v any) error {
	b, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("rawCodec: cannot unmarshal into %T", v)
	}
	*b = data.Materialize()
	return nil
}

// chanListener is a net.Listener fed by hand: the TCP server sniffs each
// connection and pushes the HTTP/2 ones here.
type chanListener struct {
	addr   net.Addr
	conns  chan net.Conn
	once   sync.Once
	closed chan struct{}
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{addr: addr, conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *chanListener) push(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.closed:
		c.Close()
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

// recordedConn replays the bytes the sniffer already read, and copies all
// traffic into the pcap (Wireshark decodes HTTP/2 and gRPC from it).
type recordedConn struct {
	net.Conn
	r             io.Reader
	cap           *Capture
	client, local netip.AddrPort
}

func (c *recordedConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.cap.PcapOnly("TCP", c.client, c.local, true, append([]byte(nil), p[:n]...))
	}
	return n, err
}

func (c *recordedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.cap.PcapOnly("TCP", c.client, c.local, false, append([]byte(nil), p[:n]...))
	}
	return n, err
}
