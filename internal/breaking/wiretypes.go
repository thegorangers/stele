package breaking

import "google.golang.org/protobuf/reflect/protoreflect"

// wireType mirrors the wire types defined by the protobuf encoding, see
// https://protobuf.dev/programming-guides/encoding/#structure.
type wireType int

const (
	wireVarint          wireType = 0
	wireFixed64         wireType = 1
	wireLengthDelimited wireType = 2
	wireFixed32         wireType = 5
)

// wireTypeOf reports the wire type a scalar kind is encoded with. Kinds not
// covered here (message, group, enum's wrapper, ...) are not part of this
// matrix.
func wireTypeOf(k protoreflect.Kind) (wireType, bool) {
	switch k {
	case protoreflect.Int32Kind, protoreflect.Int64Kind,
		protoreflect.Uint32Kind, protoreflect.Uint64Kind,
		protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.BoolKind, protoreflect.EnumKind:
		return wireVarint, true
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		return wireFixed64, true
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		return wireFixed32, true
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind:
		return wireLengthDelimited, true
	}
	return 0, false
}

// numericFamily distinguishes how a fixed-width or varint kind's bit pattern
// is interpreted as a number, since sharing a wire type only makes decoding
// succeed — it does not make the decoded value mean the same thing.
//
//   - "twos-complement": the raw bits (sign/magnitude via two's complement)
//     are the number as-is: int32, int64, sfixed32, sfixed64.
//   - "unsigned": the raw bits read as an unsigned magnitude: uint32, uint64,
//     fixed32, fixed64.
//   - "zigzag": the raw bits are zigzag-decoded before use: sint32, sint64.
//   - "ieee754": the raw bits are an IEEE-754 float/double: float, double.
//   - "bool": the raw varint is truthiness (0/nonzero), not a general number.
type numericFamily int

const (
	familyNone numericFamily = iota
	familyTwosComplement
	familyUnsigned
	familyZigzag
	familyIEEE754
	familyBool
)

func numericFamilyOf(k protoreflect.Kind) numericFamily {
	switch k {
	case protoreflect.Int32Kind, protoreflect.Int64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		return familyTwosComplement
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		return familyUnsigned
	case protoreflect.Sint32Kind, protoreflect.Sint64Kind:
		return familyZigzag
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return familyIEEE754
	case protoreflect.BoolKind:
		return familyBool
	}
	return familyNone
}

// bitWidth reports the number of bits a kind's wire representation carries,
// for the fixed-width kinds where narrowing loses information.
func bitWidth(k protoreflect.Kind) int {
	switch k {
	case protoreflect.Int32Kind, protoreflect.Uint32Kind, protoreflect.Sint32Kind,
		protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		return 32
	case protoreflect.Int64Kind, protoreflect.Uint64Kind, protoreflect.Sint64Kind,
		protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		return 64
	}
	return 0
}

// WireCompatible reports whether bytes written for a field of kind `from`
// can still be read back with the same meaning by a field of kind `to`.
//
// This is deliberately narrower than "decodes without error": float and
// fixed32 both use wire type 5 and every decode between them succeeds, but
// the four bytes of 1.0 (0x3f800000) read back as fixed32 1065353216 — the
// bytes decode, the value does not survive. Compatibility requires both the
// same wire type AND the same interpretation of the bits (numericFamily),
// with no narrowing of bit width in the read direction.
func WireCompatible(from, to protoreflect.Kind) bool {
	if from == to {
		return true
	}

	fromWT, ok1 := wireTypeOf(from)
	toWT, ok2 := wireTypeOf(to)
	if !ok1 || !ok2 || fromWT != toWT {
		return false
	}

	// string and bytes share wire type 2 (length-delimited) with each other
	// and with embedded messages, but message is out of scope for this
	// scalar matrix. The direction matters here, and this is not
	// symmetric: proto3 requires a string field's contents to be valid
	// UTF-8. Writing a bytes field and reading it back as string can
	// therefore fail (or, per
	// https://protobuf.dev/programming-guides/proto3/#scalar, produce
	// unspecified behaviour) for a byte sequence that is not valid UTF-8 —
	// which the corpus includes on purpose. Writing a string and reading it
	// back as bytes never has this problem: any UTF-8 byte sequence is also
	// a valid arbitrary byte sequence. So string -> bytes is compatible and
	// bytes -> string is not.
	if fromWT == wireLengthDelimited {
		switch {
		case from == protoreflect.StringKind && to == protoreflect.BytesKind:
			return true
		case from == protoreflect.BytesKind && to == protoreflect.StringKind:
			return false
		}
		return false
	}

	ff := numericFamilyOf(from)
	tf := numericFamilyOf(to)
	if ff == familyNone || tf == familyNone {
		return false
	}

	// bool is a varint whose payload is always the single byte 0 or 1: that
	// payload, reinterpreted as int32, int64, uint32 or uint64, is exactly
	// the number false/true already means (0 or 1) — there is no sign bit
	// or width to trip over for such a small value, so bool -> {int32,
	// int64, uint32, uint64} preserves the value in both directions of
	// "meaning" (true means 1, false means 0).
	//
	// The reverse does not hold: an arbitrary int32/int64/uint32/uint64
	// varint (say, 2) reinterpreted as bool loses everything but
	// truthiness, and 2 != true numerically. So bool is a source of
	// compatibility here but never a target from a wider plain-integer
	// kind.
	if from == protoreflect.BoolKind && tf != familyNone {
		return tf == familyTwosComplement || tf == familyUnsigned || tf == familyBool
	}
	if to == protoreflect.BoolKind {
		return from == protoreflect.BoolKind
	}

	// uint32's varint payload is the value's magnitude with no sign
	// extension: it never uses more than 32 significant bits. Reading that
	// payload back as a 64-bit two's-complement int64 can never set int64's
	// sign bit (bit 63), because the value is at most 2^32-1 « 2^63 — so
	// uint32 -> int64 preserves the value even though the two kinds are in
	// different numeric families. The narrower uint32 -> int32 does NOT
	// hold: values at or above 2^31 (well within uint32's range, and
	// present in the corpus) do flip the sign when read back as a 32-bit
	// two's complement int32.
	if from == protoreflect.Uint32Kind && to == protoreflect.Int64Kind {
		return true
	}

	if ff != tf {
		return false
	}

	// Same wire type, same family: still incompatible if the read side is
	// narrower than the write side, since a wide value truncates when read
	// as the narrower kind (e.g. int64 -> int32 loses the high bits, even
	// though both are varint/twos-complement).
	if bitWidth(to) < bitWidth(from) {
		return false
	}

	return true
}
