package main

import (
	"bytes"
	"sort"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Extract finds the serialized FileDescriptorProtos in a binary. Each one
// begins with field 1, its file name, so the search starts from every
// ".proto" string that sits right after a 0x0A tag and its length.
func Extract(data []byte) []*descriptorpb.FileDescriptorProto {
	found := map[string]*descriptorpb.FileDescriptorProto{}
	size := map[string]int{}
	suffix := []byte(".proto")
	for i := 0; ; {
		j := bytes.Index(data[i:], suffix)
		if j < 0 {
			break
		}
		end := i + j + len(suffix)
		i = end
		start, ok := nameStart(data, end)
		if !ok {
			continue
		}
		blob := descriptorExtent(data[start:])
		var fd descriptorpb.FileDescriptorProto
		if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(blob, &fd); err != nil {
			continue
		}
		name := fd.GetName()
		if name == "" || !bytes.HasSuffix(data[:end], []byte(name)) {
			continue
		}
		if len(fd.GetMessageType())+len(fd.GetEnumType())+len(fd.GetService())+len(fd.GetExtension()) == 0 {
			continue
		}
		if len(blob) > size[name] { // keep the most complete copy
			found[name], size[name] = &fd, len(blob)
		}
	}
	out := make([]*descriptorpb.FileDescriptorProto, 0, len(found))
	for _, fd := range found {
		out = append(out, fd)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].GetName() < out[b].GetName() })
	return out
}

// nameStart finds where the field holding a file name that ends at end
// begins: a 0x0A tag, the name's length as a varint, then the name, made of
// path characters only.
func nameStart(data []byte, end int) (int, bool) {
	for n := len(".proto") + 1; n <= 512 && n <= end; n++ {
		s := end - n // first byte of the name
		if !pathChar(data[s]) {
			return 0, false
		}
		var lenBytes []byte
		lenBytes = protowire.AppendVarint(lenBytes, uint64(n))
		tagAt := s - len(lenBytes) - 1
		if tagAt < 0 {
			return 0, false
		}
		if data[tagAt] == 0x0a && bytes.Equal(data[tagAt+1:s], lenBytes) {
			return tagAt, true
		}
	}
	return 0, false
}

func pathChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '/' || c == '.' || c == '-'
}

// descriptorExtent returns the longest prefix of b made of fields a
// FileDescriptorProto can hold, with the right wire types. The blob ends where
// the next bytes stop looking like one (usually a zero byte: tag 0).
func descriptorExtent(b []byte) []byte {
	want := map[protowire.Number]protowire.Type{
		1: protowire.BytesType, 2: protowire.BytesType, 3: protowire.BytesType,
		4: protowire.BytesType, 5: protowire.BytesType, 6: protowire.BytesType,
		7: protowire.BytesType, 8: protowire.BytesType, 9: protowire.BytesType,
		12: protowire.BytesType, 13: protowire.BytesType, 14: protowire.VarintType,
	}
	n := 0
	for n < len(b) {
		num, typ, tl := protowire.ConsumeTag(b[n:])
		if tl < 0 {
			break
		}
		wt, ok := want[num]
		if num == 10 || num == 11 { // public/weak dependency: varint or packed
			ok, wt = typ == protowire.VarintType || typ == protowire.BytesType, typ
		}
		if !ok || wt != typ {
			break
		}
		vl := protowire.ConsumeFieldValue(num, typ, b[n+tl:])
		if vl < 0 {
			break
		}
		n += tl + vl
	}
	return b[:n]
}
