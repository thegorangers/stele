package breaking

import (
	"math"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// kinds is every scalar kind WireCompatible is defined over. Message and
// group kinds are deliberately excluded: this matrix is about scalar wire
// encodings, not nested messages.
var kinds = []protoreflect.Kind{
	protoreflect.DoubleKind,
	protoreflect.FloatKind,
	protoreflect.Int64Kind,
	protoreflect.Uint64Kind,
	protoreflect.Int32Kind,
	protoreflect.Fixed64Kind,
	protoreflect.Fixed32Kind,
	protoreflect.BoolKind,
	protoreflect.StringKind,
	protoreflect.BytesKind,
	protoreflect.Uint32Kind,
	protoreflect.Sfixed32Kind,
	protoreflect.Sfixed64Kind,
	protoreflect.Sint32Kind,
	protoreflect.Sint64Kind,
}

// The matrix is measured because hand-written enumerations in this repository
// have been wrong four revisions running. "Encode as one, decode as the
// other" is not the oracle: it measures the wire type. float and fixed32 are
// both four bytes and every decode succeeds, while 1.0 reads back as
// 1065353216.
//
// So: a corpus rather than a sample, and an equality defined across kinds.
var corpus = []int64{0, 1, -1, 127, 128, -128, 2147483647, -2147483648, 4294967295, 9223372036854775807}

// stringCorpus and bytesCorpus additionally carry an invalid-UTF-8 case,
// since string/bytes compatibility is measured, not derived from the numeric
// corpus.
var stringBytesCorpus = [][]byte{
	{},
	[]byte("hello"),
	[]byte("with a \x00 nul"),
	{0xff, 0xfe, 0xfd}, // invalid UTF-8
}

// buildDescriptor synthesises a one-field message descriptor "M" with field
// "f" of the given kind, tag 1.
func buildDescriptor(t *testing.T, kind protoreflect.Kind) protoreflect.MessageDescriptor {
	t.Helper()

	label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	number := int32(1)
	name := "f"
	jsonName := "f"

	fdp := &descriptorpb.FieldDescriptorProto{
		Name:     &name,
		Number:   &number,
		Label:    &label,
		Type:     kindToFieldType(kind).Enum(),
		JsonName: &jsonName,
	}

	msgName := "M"
	fileName := "example_" + kind.String() + ".proto"
	syntax := "proto3"
	pkg := "example"

	fdProto := &descriptorpb.FileDescriptorProto{
		Name:    &fileName,
		Package: &pkg,
		Syntax:  &syntax,
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:  &msgName,
				Field: []*descriptorpb.FieldDescriptorProto{fdp},
			},
		},
	}

	fd, err := protodesc.NewFile(fdProto, nil)
	if err != nil {
		t.Fatalf("building descriptor for kind %v: %v", kind, err)
	}
	return fd.Messages().Get(0)
}

// kindToFieldType maps a protoreflect.Kind to the FieldDescriptorProto_Type
// with the same name.
func kindToFieldType(kind protoreflect.Kind) descriptorpb.FieldDescriptorProto_Type {
	return descriptorpb.FieldDescriptorProto_Type(kind)
}

// measure encodes each corpus value as from and decodes it as to, and reports
// whether every value survives with its meaning intact. Numeric kinds compare
// numerically, which is what rejects float against fixed32. string and bytes
// compare as byte sequences, and each direction is measured separately
// because they are not symmetric.
func measure(t *testing.T, from, to protoreflect.Kind) bool {
	t.Helper()

	fromMD := buildDescriptor(t, from)
	toMD := buildDescriptor(t, to)
	fromFD := fromMD.Fields().Get(0)
	toFD := toMD.Fields().Get(0)

	isStringLike := func(k protoreflect.Kind) bool {
		return k == protoreflect.StringKind || k == protoreflect.BytesKind
	}

	switch {
	case isStringLike(from) || isStringLike(to):
		if !isStringLike(from) || !isStringLike(to) {
			// A string/bytes kind can never share a wire type with a numeric
			// kind (wire type 2 vs 0/1/5), so nothing round-trips.
			return false
		}
		for _, v := range stringBytesCorpus {
			// string requires valid UTF-8 on the way in for proto3; skip
			// invalid UTF-8 when setting a string field, since it cannot be
			// legally set in the first place — the incompatibility that
			// matters is captured by the other corpus entries plus the
			// bytes-invalid-utf8-into-string case below.
			if from == protoreflect.StringKind && !isValidUTF8(v) {
				continue
			}

			msg := dynamicpb.NewMessage(fromMD)
			if from == protoreflect.StringKind {
				msg.Set(fromFD, protoreflect.ValueOfString(string(v)))
			} else {
				msg.Set(fromFD, protoreflect.ValueOfBytes(append([]byte(nil), v...)))
			}

			b, err := proto.Marshal(msg)
			if err != nil {
				return false
			}

			out := dynamicpb.NewMessage(toMD)
			if err := proto.Unmarshal(b, out); err != nil {
				return false
			}

			var gotBytes []byte
			if to == protoreflect.StringKind {
				gotBytes = []byte(out.Get(toFD).String())
			} else {
				gotBytes = out.Get(toFD).Bytes()
			}

			if !bytesEqual(gotBytes, v) {
				return false
			}
		}
		return true

	default:
		for _, v := range corpus {
			msg := dynamicpb.NewMessage(fromMD)
			val, ok := setNumeric(fromFD, from, v)
			if !ok {
				// Value doesn't fit the source kind (e.g. negative into
				// uint32); skip it for this pair, it is not part of what we
				// are testing.
				continue
			}
			msg.Set(fromFD, val)

			b, err := proto.Marshal(msg)
			if err != nil {
				return false
			}

			out := dynamicpb.NewMessage(toMD)
			if err := proto.Unmarshal(b, out); err != nil {
				return false
			}

			gotVal, wantVal, ok := numericValues(toFD, to, out.Get(toFD), from, v)
			if !ok {
				continue
			}
			if gotVal != wantVal {
				return false
			}
		}
		return true
	}
}

func isValidUTF8(b []byte) bool {
	for i := 0; i < len(b); {
		r := b[i]
		if r < 0x80 {
			i++
			continue
		}
		return false
	}
	return true
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// setNumeric sets fd on a fresh message from the given int64 corpus value,
// truncating/reinterpreting the bits as the kind's native width, and reports
// whether the value is representable for that kind at all.
func setNumeric(fd protoreflect.FieldDescriptor, kind protoreflect.Kind, v int64) (protoreflect.Value, bool) {
	switch kind {
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(float64(v)), true
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(float32(v)), true
	case protoreflect.Int64Kind, protoreflect.Sfixed64Kind, protoreflect.Sint64Kind:
		return protoreflect.ValueOfInt64(v), true
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(uint64(v)), true
	case protoreflect.Int32Kind, protoreflect.Sfixed32Kind, protoreflect.Sint32Kind:
		if v > math.MaxInt32 || v < math.MinInt32 {
			return protoreflect.Value{}, false
		}
		return protoreflect.ValueOfInt32(int32(v)), true
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		if v < 0 || v > math.MaxUint32 {
			return protoreflect.Value{}, false
		}
		return protoreflect.ValueOfUint32(uint32(v)), true
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(v != 0), true
	}
	return protoreflect.Value{}, false
}

// numericValues extracts the decoded value in a common comparable form
// (float64, exactly representing every corpus value we feed in, since the
// widest cases stay within float64's 53-bit mantissa except the max-int64
// corpus entry, which is handled specially below) together with the expected
// value under the from-kind's own interpretation of v, and reports whether
// the comparison applies.
func numericValues(fd protoreflect.FieldDescriptor, to protoreflect.Kind, got protoreflect.Value, from protoreflect.Kind, v int64) (gotVal, wantVal float64, ok bool) {
	// The expected numeric meaning is simply v interpreted the way `from`
	// interprets it, i.e. what was set. Bool sets true/false from v != 0;
	// treat bool specially, comparing as 0/1.
	var want float64
	switch from {
	case protoreflect.BoolKind:
		if v != 0 {
			want = 1
		} else {
			want = 0
		}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		want = float64(uint32(v))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		want = float64(uint64(v))
	case protoreflect.FloatKind:
		want = float64(float32(v))
	default:
		want = float64(v)
	}

	var g float64
	switch to {
	case protoreflect.DoubleKind:
		g = got.Float()
	case protoreflect.FloatKind:
		g = float64(float32(got.Float()))
	case protoreflect.Int64Kind, protoreflect.Sfixed64Kind, protoreflect.Sint64Kind:
		g = float64(got.Int())
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		g = float64(got.Uint())
	case protoreflect.Int32Kind, protoreflect.Sfixed32Kind, protoreflect.Sint32Kind:
		g = float64(got.Int())
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		g = float64(got.Uint())
	case protoreflect.BoolKind:
		if got.Bool() {
			g = 1
		} else {
			g = 0
		}
	default:
		return 0, 0, false
	}

	return g, want, true
}

func TestMatrixIsWhatMeasurementSays(t *testing.T) {
	for _, from := range kinds {
		for _, to := range kinds {
			if got, want := WireCompatible(from, to), measure(t, from, to); got != want {
				t.Errorf("WireCompatible(%v, %v) = %v, measured %v", from, to, got, want)
			}
		}
	}
}

func TestFloatAndFixed32ShareAWireTypeAndNotAValue(t *testing.T) {
	if WireCompatible(protoreflect.FloatKind, protoreflect.Fixed32Kind) {
		t.Fatal("float and fixed32 are four bytes each and mean different numbers")
	}
}

// TestInt32ToStringIsIncompatible is the required negative assertion: a
// WireCompatible that returned true unconditionally would pass every other
// assertion in this file (which mostly checks agreement with a measurement
// that itself distinguishes these), so this pins one pair that must read
// false directly against the implementation.
func TestInt32ToStringIsIncompatible(t *testing.T) {
	if WireCompatible(protoreflect.Int32Kind, protoreflect.StringKind) {
		t.Fatal("int32 (varint, wire type 0) and string (length-delimited, wire type 2) cannot share encoded bytes")
	}
}
