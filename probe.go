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

// ProbeReply builds the probe message: every text value labels its position.
func ProbeReply() []byte { return probeMessage(probeLabel) }

// Probe 2 (after probe 1 showed GetLoginInfoResult field 3 is the character
// list and a character's field 4 its name): every text value is the same
// label, so whichever fields hold a world's id and a character's world_id,
// they match. Only the character's name differs, to tell the runs apart.
var (
	probeSame     = []byte("@W@W@W@W")
	probeCharName = []byte("@N@a@m@e")
)

// ProbeReply2 builds probe 2.
func ProbeReply2() []byte {
	return probeMessage(func(top, inner int) []byte {
		if top == 3 && inner == 4 {
			return probeCharName
		}
		return probeSame
	})
}

// Probe 3 (after probe 2 moved the character to a world the menu doesn't
// list): which character field is world_id? Field 3 now holds one character
// per field j (j != 4, the name): only field j is @W@W@W@W, the id of the
// hidden worlds; every other field is a unique label. The character whose
// field j is world_id moves to a hidden world and drops out of the list. One
// more character has every field empty, to see whether "Local Host" is the
// world with an empty id. The other top-level fields are as in probe 2.
func ProbeReply3() []byte {
	var out []byte
	for top := 1; top <= probeTopFields; top++ {
		if top != 3 {
			out = protowire.AppendTag(out, protowire.Number(top), protowire.BytesType)
			out = protowire.AppendBytes(out, probeInner(func(int) []byte { return probeSame }))
			continue
		}
		for j := 1; j <= probeInnerFields; j++ {
			if j == 4 {
				continue
			}
			char := probeInner(func(f int) []byte {
				switch f {
				case 4:
					return []byte{'@', 'C', '@', fieldChar(j), '@', '_', '@', '_'}
				case j:
					return probeSame
				}
				return probeLabel(j, f)
			})
			out = protowire.AppendTag(out, 3, protowire.BytesType)
			out = protowire.AppendBytes(out, char)
		}
		empty := probeInner(func(f int) []byte {
			if f == 4 {
				return []byte("@E@m@p@t")
			}
			return []byte{}
		})
		out = protowire.AppendTag(out, 3, protowire.BytesType)
		out = protowire.AppendBytes(out, empty)
	}
	return out
}

// probeInner is one message with fields 1-15 set to value(field).
func probeInner(value func(f int) []byte) []byte {
	var inner []byte
	for f := 1; f <= probeInnerFields; f++ {
		inner = protowire.AppendTag(inner, protowire.Number(f), protowire.BytesType)
		inner = protowire.AppendBytes(inner, value(f))
	}
	return inner
}

// probeMessage fills fields 1-20, each with fields 1-15 holding label(top, inner).
// Every label must be 8 ASCII bytes that also parse as a message.
func probeMessage(label func(top, inner int) []byte) []byte {
	var out []byte
	for top := 1; top <= probeTopFields; top++ {
		var inner []byte
		for f := 1; f <= probeInnerFields; f++ {
			inner = protowire.AppendTag(inner, protowire.Number(f), protowire.BytesType)
			inner = protowire.AppendBytes(inner, label(top, f))
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
