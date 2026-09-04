package actionsecurity

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"testing"
)

func TestParseRootKey(t *testing.T) {
	rawBytes := []byte("0123456789abcdef0123456789abcdef")
	valid := base64.RawURLEncoding.EncodeToString(rawBytes)
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
	if _, _, err := NewTicket(bytes.NewReader(source[:31])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("NewTicket(short reader) error = %v", err)
	}
	if _, err := NewPublicRef(bytes.NewReader(source[:31])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("NewPublicRef(short reader) error = %v", err)
	}
}
