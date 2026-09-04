package actionsecurity

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestParseRootKey(t *testing.T) {
	rawBytes := []byte("0123456789abcdef0123456789abcdef")
	valid := base64.RawURLEncoding.EncodeToString(rawBytes)
	nonCanonical := nonCanonicalBase64Alias(t, valid)
	for _, tc := range []struct {
		name string
		raw  string
		want KeyErrorReason
	}{
		{name: "valid", raw: valid},
		{name: "missing", raw: "", want: KeyMissing},
		{name: "short", raw: valid[:42], want: KeyInvalidLength},
		{name: "padding", raw: valid + "=", want: KeyInvalidLength},
		{name: "leading whitespace", raw: " " + valid, want: KeyInvalidLength},
		{name: "trailing whitespace", raw: valid + " ", want: KeyInvalidLength},
		{name: "unicode", raw: "界" + valid[3:], want: KeyInvalidEncoding},
		{name: "standard base64 character", raw: valid[:42] + "+", want: KeyInvalidEncoding},
		{name: "non canonical pad bits", raw: nonCanonical, want: KeyInvalidEncoding},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := ParseRootKey(tc.raw)
			if reason != tc.want {
				t.Fatalf("ParseRootKey() reason = %q, want %q", reason, tc.want)
			}
			if tc.want == "" && !bytes.Equal(got, rawBytes) {
				t.Fatalf("ParseRootKey() decoded bytes differ")
			}
			if tc.want != "" && got != nil {
				t.Fatalf("ParseRootKey() returned bytes for rejected input")
			}
		})
	}
}

func TestExternalValueParsing(t *testing.T) {
	raw := bytes.Repeat([]byte{0xa5}, 32)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	nonCanonical := nonCanonicalBase64Alias(t, encoded)
	for _, tc := range []struct {
		name   string
		parse  func() ([32]byte, error)
		wantOK bool
	}{
		{name: "idempotency valid", parse: func() ([32]byte, error) { return ParseIdempotencyKey([]string{"ik_" + encoded}) }, wantOK: true},
		{name: "ticket valid", parse: func() ([32]byte, error) { return ParseTicket([]string{"av_" + encoded}) }, wantOK: true},
		{name: "public ref valid", parse: func() ([32]byte, error) { return ParsePublicRef("op_" + encoded) }, wantOK: true},
		{name: "missing header", parse: func() ([32]byte, error) { return ParseIdempotencyKey(nil) }},
		{name: "duplicate header", parse: func() ([32]byte, error) { return ParseTicket([]string{"av_" + encoded, "av_" + encoded}) }},
		{name: "comma joined", parse: func() ([32]byte, error) { return ParseIdempotencyKey([]string{"ik_" + encoded + ",ik_" + encoded}) }},
		{name: "no leading trim", parse: func() ([32]byte, error) { return ParseTicket([]string{" av_" + encoded}) }},
		{name: "no trailing trim", parse: func() ([32]byte, error) { return ParseIdempotencyKey([]string{"ik_" + encoded + " "}) }},
		{name: "case sensitive prefix", parse: func() ([32]byte, error) { return ParseIdempotencyKey([]string{"IK_" + encoded}) }},
		{name: "wrong prefix", parse: func() ([32]byte, error) { return ParsePublicRef("av_" + encoded) }},
		{name: "unicode body", parse: func() ([32]byte, error) { return ParseTicket([]string{"av_界" + encoded[3:]}) }},
		{name: "padded body", parse: func() ([32]byte, error) { return ParsePublicRef("op_" + encoded[:42] + "=") }},
		{name: "idempotency non canonical pad bits", parse: func() ([32]byte, error) { return ParseIdempotencyKey([]string{"ik_" + nonCanonical}) }},
		{name: "ticket non canonical pad bits", parse: func() ([32]byte, error) { return ParseTicket([]string{"av_" + nonCanonical}) }},
		{name: "public ref non canonical pad bits", parse: func() ([32]byte, error) { return ParsePublicRef("op_" + nonCanonical) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.parse()
			if tc.wantOK {
				if err != nil {
					t.Fatalf("parse error = %v", err)
				}
				if !bytes.Equal(got[:], raw) {
					t.Fatal("decoded value differs")
				}
				return
			}
			if err == nil {
				t.Fatal("invalid external value was accepted")
			}
		})
	}
}

func TestExternalValueGeneration(t *testing.T) {
	source := bytes.Repeat([]byte{0x5a}, 64)
	ticket, ticketRaw, err := NewTicket(bytes.NewReader(source[:32]))
	if err != nil || len(ticket) != 46 || ticket[:3] != "av_" || !bytes.Equal(ticketRaw[:], source[:32]) {
		t.Fatalf("NewTicket() returned invalid result: len=%d prefix_ok=%t err=%v", len(ticket), len(ticket) >= 3 && ticket[:3] == "av_", err)
	}
	publicRef, err := NewPublicRef(bytes.NewReader(source[32:]))
	if err != nil || len(publicRef) != 46 || publicRef[:3] != "op_" {
		t.Fatalf("NewPublicRef() returned invalid result: len=%d prefix_ok=%t err=%v", len(publicRef), len(publicRef) >= 3 && publicRef[:3] == "op_", err)
	}
	if _, _, err := NewTicket(bytes.NewReader(source[:31])); !errors.Is(err, errRandomSource) {
		t.Fatalf("NewTicket(short reader) error = %v", err)
	}
	if _, err := NewPublicRef(bytes.NewReader(source[:31])); !errors.Is(err, errRandomSource) {
		t.Fatalf("NewPublicRef(short reader) error = %v", err)
	}
}

func TestExternalValueGenerationRejectsNilAndClearsPartialRandomness(t *testing.T) {
	if _, _, err := NewTicket(nil); !errors.Is(err, errRandomSource) {
		t.Fatalf("NewTicket(nil) error = %v", err)
	}
	if _, err := NewPublicRef(nil); !errors.Is(err, errRandomSource) {
		t.Fatalf("NewPublicRef(nil) error = %v", err)
	}
	var typedNil *capturingErrorReader
	if _, _, err := NewTicket(typedNil); !errors.Is(err, errRandomSource) {
		t.Fatalf("NewTicket(typed nil) error = %v", err)
	}

	for _, tc := range []struct {
		name string
		call func(*capturingErrorReader) error
	}{
		{name: "ticket", call: func(reader *capturingErrorReader) error { _, _, err := NewTicket(reader); return err }},
		{name: "public ref", call: func(reader *capturingErrorReader) error { _, err := NewPublicRef(reader); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &capturingErrorReader{}
			if err := tc.call(reader); !errors.Is(err, errRandomSource) {
				t.Fatalf("generation error = %v", err)
			}
			if len(reader.captured) == 0 {
				t.Fatal("reader did not capture destination buffer")
			}
			if !bytes.Equal(reader.captured, make([]byte, len(reader.captured))) {
				t.Fatal("partial random bytes were not cleared")
			}
		})
	}
}

type capturingErrorReader struct {
	captured []byte
}

func (r *capturingErrorReader) Read(destination []byte) (int, error) {
	r.captured = destination
	copy(destination, []byte{1, 2, 3})
	return 3, errors.New("reader detail must not escape")
}

func nonCanonicalBase64Alias(t *testing.T, canonical string) string {
	t.Helper()
	want, err := base64.RawURLEncoding.DecodeString(canonical)
	if err != nil {
		t.Fatalf("decode canonical fixture: %v", err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := range alphabet {
		candidate := canonical[:len(canonical)-1] + string(alphabet[i])
		if candidate == canonical {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(candidate)
		if err == nil && bytes.Equal(decoded, want) {
			if _, strictErr := base64.RawURLEncoding.Strict().DecodeString(candidate); strictErr == nil {
				t.Fatal("fixture alias unexpectedly accepted by strict decoder")
			}
			return candidate
		}
	}
	t.Fatal("could not construct non-canonical base64 alias")
	return ""
}
