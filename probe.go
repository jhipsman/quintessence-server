package main

import (
	"google.golang.org/protobuf/encoding/protowire"
)

// The probe reply finds where a response keeps its lists and its text fields
// without knowing its schema. It is a message that fills every field number,
// two levels deep, with values that are safe whatever the real field is:
//
//   - Every length-delimited value is pure ASCII, so it is valid UTF-8 for a
//     string field, and it is also a well-formed message for a message field
//     (and whole varints for a packed repeated integer field).
//   - A value whose wire type doesn't match the real field is kept as an
//     unknown field and skipped, so wrong guesses cost nothing.
//   - The innermost values are labels of their own position: "@3@2@_@_" is
//     top-level field 3, then field 2. Whatever the game displays tells us
//     which field holds it.
//
// A label is pairs of a tag byte '@' (field 8, varint) and a value byte, so as
// a message it is just field 8 set four times; as text it reads as the path.
// It is 8 bytes long so it also fits a packed repeated fixed32 or fixed64.

// probeTopFields is how many fields the top level fills. The top level of
// GetLoginInfoResult holds no string fields, so it can use field numbers past
// 15, whose two-byte tags are not ASCII.
const (
	probeTopFields   = 20
	probeInnerFields = 15 // one-byte tags: keeps the inner messages pure ASCII
)

// ProbeReply builds the probe message.
func ProbeReply() []byte {
	var out []byte
	for top := 1; top <= probeTopFields; top++ {
		var inner []byte
		for f := 1; f <= probeInnerFields; f++ {
			inner = protowire.AppendTag(inner, protowire.Number(f), protowire.BytesType)
			inner = protowire.AppendBytes(inner, probeLabel(top, f))
		}
		out = protowire.AppendTag(out, protowire.Number(top), protowire.BytesType)
		out = protowire.AppendBytes(out, inner)
	}
	return out
}

// probeLabel is the 8-byte label of a position: "@" + field, "@" + field, then
// "@_" padding.
func probeLabel(top, inner int) []byte {
	return []byte{'@', fieldChar(top), '@', fieldChar(inner), '@', '_', '@', '_'}
}

// fieldChar writes a field number as one character: 1-9, then a-k for 10-20.
func fieldChar(n int) byte {
	if n < 10 {
		return byte('0' + n)
	}
	return byte('a' + n - 10)
}
