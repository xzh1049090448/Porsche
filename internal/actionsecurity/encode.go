package actionsecurity

import (
	"encoding/binary"
	"errors"
	"math"
)

const (
	typeNull   byte = 0
	typeString byte = 1
	typeBytes  byte = 2
	typeInt64  byte = 3
	typeInt32  byte = 4
	typeArray  byte = 5
)

var errIntentEncodingTooLarge = errors.New("action intent encoding too large")

func checkedU32Length(length uint64) (uint32, error) {
	if length > math.MaxUint32 {
		return 0, errIntentEncodingTooLarge
	}
	return uint32(length), nil
}

func checkedArrayPayloadLength(count uint64, lengths []uint64) (uint32, error) {
	if _, err := checkedU32Length(count); err != nil || count != uint64(len(lengths)) {
		return 0, errIntentEncodingTooLarge
	}
	total := uint64(4)
	for _, length := range lengths {
		var err error
		total, err = addArrayItemLength(total, length)
		if err != nil {
			return 0, err
		}
	}
	return checkedU32Length(total)
}

func addArrayItemLength(total, itemLength uint64) (uint64, error) {
	if total > math.MaxUint64-4 || itemLength > math.MaxUint64-total-4 {
		return 0, errIntentEncodingTooLarge
	}
	total += 4 + itemLength
	if total > math.MaxUint32 {
		return 0, errIntentEncodingTooLarge
	}
	return total, nil
}

type intentWriter struct{ buffer []byte }

func (w *intentWriter) fieldHeader(tag, typ byte, length uint64) error {
	checked, err := checkedU32Length(length)
	if err != nil {
		return err
	}
	w.buffer = append(w.buffer, tag, typ)
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], checked)
	w.buffer = append(w.buffer, encoded[:]...)
	return nil
}

func (w *intentWriter) field(tag, typ byte, value []byte) error {
	if err := w.fieldHeader(tag, typ, uint64(len(value))); err != nil {
		return err
	}
	w.buffer = append(w.buffer, value...)
	return nil
}

func (w *intentWriter) fieldString(tag byte, value string) error {
	if _, err := checkedU32Length(uint64(len(value))); err != nil {
		return err
	}
	return w.field(tag, typeString, []byte(value))
}

func (w *intentWriter) fieldNullableString(tag byte, value *string) error {
	if value == nil {
		return w.field(tag, typeNull, nil)
	}
	return w.fieldString(tag, *value)
}

func (w *intentWriter) fieldInt64(tag byte, value int64) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	return w.field(tag, typeInt64, encoded[:])
}

func (w *intentWriter) fieldInt32(tag byte, value int) error {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], uint32(value))
	return w.field(tag, typeInt32, encoded[:])
}

func (w *intentWriter) fieldBytes(tag byte, value []byte) error {
	return w.field(tag, typeBytes, value)
}

func (w *intentWriter) fieldArray(tag byte, values [][]byte) error {
	checkedCount, err := checkedU32Length(uint64(len(values)))
	if err != nil {
		return err
	}
	payloadLength := uint64(4)
	for _, value := range values {
		payloadLength, err = addArrayItemLength(payloadLength, uint64(len(value)))
		if err != nil {
			return err
		}
	}
	checkedLengths := make([]uint32, len(values))
	for index, value := range values {
		checkedLengths[index], err = checkedU32Length(uint64(len(value)))
		if err != nil {
			return err
		}
	}
	if err := w.fieldHeader(tag, typeArray, payloadLength); err != nil {
		return err
	}
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], checkedCount)
	w.buffer = append(w.buffer, encoded[:]...)
	for index, value := range values {
		binary.BigEndian.PutUint32(encoded[:], checkedLengths[index])
		w.buffer = append(w.buffer, encoded[:]...)
		w.buffer = append(w.buffer, value...)
	}
	return nil
}

func (w *intentWriter) bytes() []byte { return w.buffer }

func encodeIntent(write func(*intentWriter) error) ([]byte, error) {
	var writer intentWriter
	if err := write(&writer); err != nil {
		clear(writer.buffer)
		return nil, err
	}
	return writer.bytes(), nil
}

func checkedStringArrayPayloadLength(values []string) error {
	if _, err := checkedU32Length(uint64(len(values))); err != nil {
		return err
	}
	total := uint64(4)
	for _, value := range values {
		var err error
		total, err = addArrayItemLength(total, uint64(len(value)))
		if err != nil {
			return err
		}
	}
	return nil
}
