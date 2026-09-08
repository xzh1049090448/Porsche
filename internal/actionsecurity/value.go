package actionsecurity

import (
	"encoding/base64"
	"errors"
	"io"
	"reflect"
)

type KeyErrorReason string

const (
	KeyMissing         KeyErrorReason = "missing"
	KeyInvalidLength   KeyErrorReason = "invalid_length"
	KeyInvalidEncoding KeyErrorReason = "invalid_encoding"
	KeyReuse           KeyErrorReason = "key_reuse"
)

var (
	errInvalidExternalValue = errors.New("invalid external value")
	errRandomSource         = errors.New("random source unavailable")
)

var strictRawURLEncoding = base64.RawURLEncoding.Strict()

func ParseRootKey(raw string) ([]byte, KeyErrorReason) {
	if raw == "" {
		return nil, KeyMissing
	}
	if len(raw) != 43 {
		return nil, KeyInvalidLength
	}
	decoded, err := strictRawURLEncoding.DecodeString(raw)
	if err != nil {
		clear(decoded)
		return nil, KeyInvalidEncoding
	}
	if len(decoded) != 32 {
		clear(decoded)
		return nil, KeyInvalidEncoding
	}
	return decoded, ""
}

func ParseIdempotencyKey(values []string) ([32]byte, error) {
	return parseHeaderValue(values, "ik_")
}

func ParseTicket(values []string) ([32]byte, error) {
	return parseHeaderValue(values, "av_")
}

func ParsePublicRef(raw string) ([32]byte, error) {
	return parseExternalValue(raw, "op_")
}

func NewTicket(random io.Reader) (string, [32]byte, error) {
	var raw [32]byte
	defer clear(raw[:])
	if err := readRandom(random, raw[:]); err != nil {
		return "", [32]byte{}, errRandomSource
	}
	return "av_" + base64.RawURLEncoding.EncodeToString(raw[:]), raw, nil
}

func NewPublicRef(random io.Reader) (string, error) {
	var raw [32]byte
	defer clear(raw[:])
	if err := readRandom(random, raw[:]); err != nil {
		return "", errRandomSource
	}
	return "op_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func readRandom(random io.Reader, destination []byte) error {
	if isNilReader(random) {
		clear(destination)
		return errRandomSource
	}
	if _, err := io.ReadFull(random, destination); err != nil {
		clear(destination)
		return errRandomSource
	}
	return nil
}

func isNilReader(random io.Reader) bool {
	if random == nil {
		return true
	}
	value := reflect.ValueOf(random)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func parseHeaderValue(values []string, prefix string) ([32]byte, error) {
	if len(values) != 1 {
		return [32]byte{}, errInvalidExternalValue
	}
	return parseExternalValue(values[0], prefix)
}

func parseExternalValue(raw, prefix string) ([32]byte, error) {
	if len(raw) != 46 || raw[:3] != prefix {
		return [32]byte{}, errInvalidExternalValue
	}
	decoded, err := strictRawURLEncoding.DecodeString(raw[3:])
	if err != nil || len(decoded) != 32 {
		clear(decoded)
		return [32]byte{}, errInvalidExternalValue
	}
	var out [32]byte
	copy(out[:], decoded)
	clear(decoded)
	return out, nil
}
