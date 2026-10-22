package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// DumpProtobuf decodes a protobuf message without its schema: field numbers,
// wire types and values. A length-delimited field is shown as a nested
// message when it parses as one, as text when it is printable, else as hex.
// Field names are unknown; the numbers are what we learn the schema from.
// It returns nil when b is not a valid protobuf message.
func DumpProtobuf(b []byte) []string {
	lines, ok := dumpProto(b, 0)
	if !ok {
		return nil
	}
	return lines
}

const maxProtoDepth = 12

func dumpProto(b []byte, depth int) ([]string, bool) {
	var out []string
	pad := strings.Repeat("  ", depth)
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 || key>>3 == 0 || key>>3 > 1<<29-1 {
			return nil, false
		}
		b = b[n:]
		field, wire := key>>3, key&7
		switch wire {
		case 0: // varint
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, false
			}
			b = b[n:]
			s := fmt.Sprintf("%s%d: %d", pad, field, v)
			if int64(v) < 0 {
				s += fmt.Sprintf(" (int64 %d)", int64(v))
			}
			out = append(out, s)
		case 1: // 64-bit
			if len(b) < 8 {
				return nil, false
			}
			v := binary.LittleEndian.Uint64(b)
			b = b[8:]
			out = append(out, fmt.Sprintf("%s%d: fixed64 %d (double %g)", pad, field, v, math.Float64frombits(v)))
		case 5: // 32-bit
			if len(b) < 4 {
				return nil, false
			}
			v := binary.LittleEndian.Uint32(b)
			b = b[4:]
			out = append(out, fmt.Sprintf("%s%d: fixed32 %d (float %g)", pad, field, v, math.Float32frombits(v)))
		case 2: // length-delimited
			ln, n := binary.Uvarint(b)
			if n <= 0 || ln > uint64(len(b)-n) {
				return nil, false
			}
			v := b[n : n+int(ln)]
			b = b[n+int(ln):]
			out = append(out, dumpBytesField(pad, field, v, depth)...)
		default: // groups (3, 4) are long dead; treat as not protobuf
			return nil, false
		}
	}
	return out, true
}

func dumpBytesField(pad string, field uint64, v []byte, depth int) []string {
	head := fmt.Sprintf("%s%d:", pad, field)
	if len(v) == 0 {
		return []string{head + ` ""`}
	}
	if printable(v) {
		return []string{fmt.Sprintf("%s %q", head, v)}
	}
	if depth < maxProtoDepth {
		if sub, ok := dumpProto(v, depth+1); ok {
			return append([]string{head + " {"}, append(sub, pad+"}")...)
		}
	}
	if len(v) > 64 {
		return []string{fmt.Sprintf("%s %d bytes: % x …", head, len(v), v[:64])}
	}
	return []string{fmt.Sprintf("%s bytes % x", head, v)}
}

// printable is text a person would have written: valid UTF-8, no control
// characters except tab and newlines.
func printable(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0x7f {
			return false
		}
	}
	return true
}
