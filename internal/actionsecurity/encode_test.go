package actionsecurity

import (
	"bytes"
	"encoding/binary"
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
	w.fieldString(1, "A")
	w.fieldNullableString(2, nil)
	w.fieldInt64(3, 0x0102030405060708)
	w.fieldInt32(4, 0x01020304)
	w.fieldBytes(5, []byte{0xaa, 0xbb})
	w.fieldArray(6, [][]byte{{0x61}, {0x62, 0x63}})
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
