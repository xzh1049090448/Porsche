package actionsecurity

import "encoding/binary"

const (
	typeNull   byte = 0
	typeString byte = 1
	typeBytes  byte = 2
	typeInt64  byte = 3
	typeInt32  byte = 4
	typeArray  byte = 5
)

type intentWriter struct{ buffer []byte }

func (w *intentWriter) field(tag, typ byte, value []byte) {
	w.buffer = append(w.buffer, tag, typ)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	w.buffer = append(w.buffer, length[:]...)
	w.buffer = append(w.buffer, value...)
}

func (w *intentWriter) fieldString(tag byte, value string) { w.field(tag, typeString, []byte(value)) }
func (w *intentWriter) fieldNullableString(tag byte, value *string) {
	if value == nil {
		w.field(tag, typeNull, nil)
		return
	}
	w.fieldString(tag, *value)
}
func (w *intentWriter) fieldInt64(tag byte, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	w.field(tag, typeInt64, encoded[:])
}
func (w *intentWriter) fieldInt32(tag byte, value int) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], uint32(value))
	w.field(tag, typeInt32, encoded[:])
}
func (w *intentWriter) fieldBytes(tag byte, value []byte) { w.field(tag, typeBytes, value) }
func (w *intentWriter) fieldArray(tag byte, values [][]byte) {
	length := 4
	for _, value := range values {
		length += 4 + len(value)
	}
	payload := make([]byte, 4, length)
	binary.BigEndian.PutUint32(payload, uint32(len(values)))
	var itemLength [4]byte
	for _, value := range values {
		binary.BigEndian.PutUint32(itemLength[:], uint32(len(value)))
		payload = append(payload, itemLength[:]...)
		payload = append(payload, value...)
	}
	w.field(tag, typeArray, payload)
}
func (w *intentWriter) bytes() []byte { return w.buffer }
