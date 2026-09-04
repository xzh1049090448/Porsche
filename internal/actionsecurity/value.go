package actionsecurity

import (
	"encoding/base64"
	"errors"
	"io"
)

type KeyErrorReason string

const (
	KeyMissing         KeyErrorReason = "missing"
	KeyInvalidLength   KeyErrorReason = "invalid_length"
	KeyInvalidEncoding KeyErrorReason = "invalid_encoding"
	KeyReuse           KeyErrorReason = "key_reuse"
)

var errInvalidExternalValue = errors.New("invalid external value")

func ParseRootKey(raw string) ([]byte, KeyErrorReason) {
	if raw == "" {
		return nil, KeyMissing
	}
	if len(raw) != 43 {
		return nil, KeyInvalidLength
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, KeyInvalidEncoding
	}
	if len(decoded) != 32 {
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
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", [32]byte{}, err
	}
	return "av_" + base64.RawURLEncoding.EncodeToString(raw[:]), raw, nil
}

func NewPublicRef(random io.Reader) (string, error) {
	var raw [32]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", err
	}
	return "op_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
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
	decoded, err := base64.RawURLEncoding.DecodeString(raw[3:])
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, errInvalidExternalValue
	}
	var out [32]byte
	copy(out[:], decoded)
	clear(decoded)
	return out, nil
}
