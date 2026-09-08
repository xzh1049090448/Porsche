package actionsecurity

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

type decodedField struct {
	tag, typ byte
	value    []byte
}

func decodeFields(t *testing.T, encoded []byte) []decodedField {
	t.Helper()
	var fields []decodedField
	for len(encoded) > 0 {
		if len(encoded) < 6 {
			t.Fatalf("truncated field header: %x", encoded)
		}
		length := int(binary.BigEndian.Uint32(encoded[2:6]))
		if len(encoded) < 6+length {
			t.Fatalf("truncated field value: length=%d available=%d", length, len(encoded)-6)
		}
		fields = append(fields, decodedField{encoded[0], encoded[1], append([]byte(nil), encoded[6:6+length]...)})
		encoded = encoded[6+length:]
	}
	return fields
}

func TestEncodeWriterGoldenTagTypeLengthAndFixedWidth(t *testing.T) {
	var w intentWriter
	for _, err := range []error{
		w.fieldString(1, "A"),
		w.fieldNullableString(2, nil),
		w.fieldInt64(3, 0x0102030405060708),
		w.fieldInt32(4, 0x01020304),
		w.fieldBytes(5, []byte{0xaa, 0xbb}),
		w.fieldArray(6, [][]byte{{0x61}, {0x62, 0x63}}),
	} {
		if err != nil {
			t.Fatalf("small field encode failed: %v", err)
		}
	}
	want := []byte{
		1, typeString, 0, 0, 0, 1, 'A',
		2, typeNull, 0, 0, 0, 0,
		3, typeInt64, 0, 0, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8,
		4, typeInt32, 0, 0, 0, 4, 1, 2, 3, 4,
		5, typeBytes, 0, 0, 0, 2, 0xaa, 0xbb,
		6, typeArray, 0, 0, 0, 15, 0, 0, 0, 2, 0, 0, 0, 1, 0x61, 0, 0, 0, 2, 0x62, 0x63,
	}
	if got := w.bytes(); !bytes.Equal(got, want) {
		t.Fatalf("encoded writer = %x, want %x", got, want)
	}
}

func TestCheckedU32LengthBoundaries(t *testing.T) {
	if got, err := checkedU32Length(math.MaxUint32); err != nil || got != math.MaxUint32 {
		t.Fatalf("MaxUint32 rejected: got=%d err=%v", got, err)
	}
	if _, err := checkedU32Length(uint64(math.MaxUint32) + 1); err == nil {
		t.Fatal("MaxUint32+1 accepted")
	}
}

func TestCheckedArrayPayloadLengthBoundariesAndOverflow(t *testing.T) {
	if got, err := checkedArrayPayloadLength(1, []uint64{uint64(math.MaxUint32) - 8}); err != nil || got != math.MaxUint32 {
		t.Fatalf("exact MaxUint32 array rejected: got=%d err=%v", got, err)
	}
	for _, tc := range []struct {
		name    string
		count   uint64
		lengths []uint64
	}{
		{"payload max plus one", 1, []uint64{uint64(math.MaxUint32) - 7}},
		{"uint64 cumulative overflow", 1, []uint64{math.MaxUint64}},
		{"extreme count", uint64(math.MaxUint32) + 1, nil},
		{"count mismatch", 2, []uint64{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := checkedArrayPayloadLength(tc.count, tc.lengths); err == nil {
				t.Fatal("invalid array payload length accepted")
			}
		})
	}
}

func TestWriterRejectsOversizedHeaderWithoutMutationOrPanic(t *testing.T) {
	w := intentWriter{buffer: []byte{0xaa}}
	if err := w.fieldHeader(1, typeString, uint64(math.MaxUint32)+1); err == nil {
		t.Fatal("oversized field header accepted")
	}
	if !bytes.Equal(w.buffer, []byte{0xaa}) {
		t.Fatalf("writer mutated on rejected header: %x", w.buffer)
	}
}

func TestEncodeIntentClearsPartialWriterOnPropagatedError(t *testing.T) {
	var written []byte
	encoded, err := encodeIntent(func(w *intentWriter) error {
		if err := w.fieldString(1, "partial"); err != nil {
			return err
		}
		written = w.buffer
		return w.fieldHeader(2, typeString, uint64(math.MaxUint32)+1)
	})
	if err == nil || encoded != nil {
		t.Fatalf("partial writer error not propagated: encoded=%x err=%v", encoded, err)
	}
	if !bytes.Equal(written, make([]byte, len(written))) {
		t.Fatalf("partial writer buffer not cleared: %x", written)
	}
}
