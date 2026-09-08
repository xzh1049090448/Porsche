package dto

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDecodeAdminUserStatusValidTransitions(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantStatus  string
		wantReason  *string
		wantVersion int
	}{
		{name: "enable", body: `{"status":"active","reason":null,"expected_auth_version":1}`, wantStatus: "active", wantVersion: 1},
		{name: "disable", body: `{"status":"disabled","reason":"security review","expected_auth_version":7}`, wantStatus: "disabled", wantReason: statusStringPointer("security review"), wantVersion: 7},
		{name: "normalizes reason", body: `{"reason":"  needs review \n","expected_auth_version":2147483647,"status":"disabled"}`, wantStatus: "disabled", wantReason: statusStringPointer("needs review"), wantVersion: 2147483647},
		{name: "unicode boundary", body: fmt.Sprintf(`{"status":"disabled","reason":%q,"expected_auth_version":2}`, strings.Repeat("界", 200)), wantStatus: "disabled", wantReason: statusStringPointer(strings.Repeat("界", 200)), wantVersion: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeAdminUserStatus(strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != test.wantStatus || got.ExpectedAuthVersion != test.wantVersion || (got.Reason == nil) != (test.wantReason == nil) {
				t.Fatalf("decoded=%+v", got)
			}
			if got.Reason != nil && *got.Reason != *test.wantReason {
				t.Fatalf("reason=%q want=%q", *got.Reason, *test.wantReason)
			}
		})
	}
}

func TestDecodeAdminUserStatusOwnsNormalizedReason(t *testing.T) {
	source := []byte(`{"status":"disabled","reason":"  investigate  ","expected_auth_version":9}`)
	got, err := DecodeAdminUserStatus(bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	clear(source)
	if got.Reason == nil || *got.Reason != "investigate" {
		t.Fatalf("decoded=%+v", got)
	}
}

func TestDecodeAdminUserStatusRejectsMalformedAndUnknownJSON(t *testing.T) {
	valid := `{"status":"active","reason":null,"expected_auth_version":7}`
	if _, err := DecodeAdminUserStatus(nil); !errors.Is(err, ErrAdminUserStatusInvalidBody) {
		t.Fatalf("nil body error=%v", err)
	}
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty", body: nil}, {name: "array", body: []byte(`[]`)}, {name: "null", body: []byte(`null`)},
		{name: "trailing", body: []byte(valid + `{}`)},
		{name: "duplicate status", body: []byte(`{"status":"active","status":"disabled","reason":null,"expected_auth_version":7}`)},
		{name: "duplicate reason", body: []byte(`{"status":"active","reason":null,"reason":null,"expected_auth_version":7}`)},
		{name: "duplicate version", body: []byte(`{"status":"active","reason":null,"expected_auth_version":7,"expected_auth_version":8}`)},
		{name: "case folded collision", body: []byte(`{"status":"active","Status":"active","reason":null,"expected_auth_version":7}`)},
		{name: "case folded reason", body: []byte(`{"status":"active","reason":null,"Reason":null,"expected_auth_version":7}`)},
		{name: "case folded version", body: []byte(`{"status":"active","reason":null,"expected_auth_version":7,"Expected_Auth_Version":8}`)},
		{name: "escaped folded collision", body: []byte(`{"status":"active","\u0053tatus":"active","reason":null,"expected_auth_version":7}`)},
		{name: "unknown", body: []byte(`{"status":"active","reason":null,"expected_auth_version":7,"role":"admin"}`)},
		{name: "invalid utf8", body: append([]byte(`{"status":"disabled","reason":"`), append([]byte{0xff}, []byte(`","expected_auth_version":7}`)...)...)},
		{name: "lone high surrogate", body: []byte(`{"status":"disabled","reason":"\ud800","expected_auth_version":7}`)},
		{name: "lone low surrogate", body: []byte(`{"status":"disabled","reason":"\udc00","expected_auth_version":7}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeAdminUserStatus(bytes.NewReader(test.body)); !errors.Is(err, ErrAdminUserStatusInvalidBody) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDecodeAdminUserStatusRejectsMissingAndInvalidFields(t *testing.T) {
	tests := map[string]string{
		"missing status":        `{"reason":null,"expected_auth_version":7}`,
		"missing reason":        `{"status":"active","expected_auth_version":7}`,
		"missing version":       `{"status":"active","reason":null}`,
		"null status":           `{"status":null,"reason":null,"expected_auth_version":7}`,
		"unknown status":        `{"status":"paused","reason":null,"expected_auth_version":7}`,
		"active string reason":  `{"status":"active","reason":"unused","expected_auth_version":7}`,
		"disabled null reason":  `{"status":"disabled","reason":null,"expected_auth_version":7}`,
		"disabled empty reason": `{"status":"disabled","reason":" \t\n ","expected_auth_version":7}`,
		"disabled long reason":  fmt.Sprintf(`{"status":"disabled","reason":%q,"expected_auth_version":7}`, strings.Repeat("界", 201)),
		"version null":          `{"status":"active","reason":null,"expected_auth_version":null}`,
		"version zero":          `{"status":"active","reason":null,"expected_auth_version":0}`,
		"version negative":      `{"status":"active","reason":null,"expected_auth_version":-1}`,
		"version float":         `{"status":"active","reason":null,"expected_auth_version":1.0}`,
		"version exponent":      `{"status":"active","reason":null,"expected_auth_version":1e1}`,
		"version overflow":      `{"status":"active","reason":null,"expected_auth_version":2147483648}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAdminUserStatus(strings.NewReader(body)); !errors.Is(err, ErrAdminUserStatusInvalidBody) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDecodeAdminUserStatusEnforcesBodyLimit(t *testing.T) {
	valid := []byte(`{"status":"active","reason":null,"expected_auth_version":1}`)
	exact := append(append([]byte(nil), valid...), bytes.Repeat([]byte(" "), int(AdminUserStatusBodyLimit)-len(valid))...)
	if _, err := DecodeAdminUserStatus(bytes.NewReader(exact)); err != nil {
		t.Fatalf("exact body limit: %v", err)
	}
	if _, err := DecodeAdminUserStatus(bytes.NewReader(append(exact, ' '))); !errors.Is(err, ErrAdminUserStatusBodyTooLarge) {
		t.Fatalf("oversized error=%v", err)
	}
}

func TestDecodeAdminUserStatusCountsUnicodeCodePoints(t *testing.T) {
	reason := strings.Repeat("😀", 200)
	if utf8.RuneCountInString(reason) != 200 {
		t.Fatal("bad fixture")
	}
	got, err := DecodeAdminUserStatus(strings.NewReader(fmt.Sprintf(`{"status":"disabled","reason":%q,"expected_auth_version":1}`, reason)))
	if err != nil || got.Reason == nil || *got.Reason != reason {
		t.Fatalf("decoded=%+v error=%v", got, err)
	}
}

func statusStringPointer(value string) *string { return &value }
