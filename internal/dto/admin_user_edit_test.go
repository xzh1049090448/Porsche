package dto

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDecodeAdminUserEditValidObjects(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantNickname *string
		wantClear    bool
		wantVersion  int
	}{
		{name: "string", body: `{"nickname":"Alice","expected_auth_version":1}`, wantNickname: stringPointer("Alice"), wantVersion: 1},
		{name: "null clears", body: `{"nickname":null,"expected_auth_version":7}`, wantClear: true, wantVersion: 7},
		{name: "normalizes whitespace", body: `{"expected_auth_version":2147483647,"nickname":"  Alice Chen \n"}`, wantNickname: stringPointer("Alice Chen"), wantVersion: 2147483647},
		{name: "one unicode code point", body: `{"nickname":"界","expected_auth_version":2}`, wantNickname: stringPointer("界"), wantVersion: 2},
		{name: "sixty four unicode code points", body: fmt.Sprintf(`{"nickname":%q,"expected_auth_version":3}`, strings.Repeat("界", 64)), wantNickname: stringPointer(strings.Repeat("界", 64)), wantVersion: 3},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeAdminUserEdit(strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if got.ClearNickname != test.wantClear || got.ExpectedAuthVersion != test.wantVersion {
				t.Fatalf("decoded=%+v", got)
			}
			if (got.Nickname == nil) != (test.wantNickname == nil) {
				t.Fatalf("nickname=%v want=%v", got.Nickname, test.wantNickname)
			}
			if got.Nickname != nil && *got.Nickname != *test.wantNickname {
				t.Fatalf("nickname=%q want=%q", *got.Nickname, *test.wantNickname)
			}
		})
	}
}

func TestDecodeAdminUserEditOwnsNormalizedNickname(t *testing.T) {
	source := []byte(`{"nickname":"  Alice  ","expected_auth_version":9}`)
	got, err := DecodeAdminUserEdit(bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	clear(source)
	if got.Nickname == nil || *got.Nickname != "Alice" || got.ClearNickname {
		t.Fatalf("decoded=%+v", got)
	}
}

func TestDecodeAdminUserEditRejectsStrictJSON(t *testing.T) {
	valid := `{"nickname":"Alice","expected_auth_version":7}`
	if _, err := DecodeAdminUserEdit(nil); !errors.Is(err, ErrAdminUserEditInvalidBody) {
		t.Fatalf("nil body error=%v", err)
	}
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty", body: []byte("")},
		{name: "array", body: []byte(`[{"nickname":"Alice","expected_auth_version":7}]`)},
		{name: "scalar", body: []byte(`true`)},
		{name: "null object", body: []byte(`null`)},
		{name: "trailing object", body: []byte(valid + `{}`)},
		{name: "trailing scalar", body: []byte(valid + ` true`)},
		{name: "duplicate nickname", body: []byte(`{"nickname":"Alice","nickname":"Bob","expected_auth_version":7}`)},
		{name: "duplicate version", body: []byte(`{"nickname":"Alice","expected_auth_version":7,"expected_auth_version":8}`)},
		{name: "case folded nickname", body: []byte(`{"nickname":"Alice","Nickname":"Bob","expected_auth_version":7}`)},
		{name: "case folded version", body: []byte(`{"nickname":"Alice","expected_auth_version":7,"Expected_Auth_Version":8}`)},
		{name: "escaped duplicate", body: []byte(`{"nickname":"Alice","\u006eickname":"Bob","expected_auth_version":7}`)},
		{name: "escaped case folded collision", body: []byte(`{"nickname":"Alice","\u004eickname":"Bob","expected_auth_version":7}`)},
		{name: "unknown", body: []byte(`{"nickname":"Alice","expected_auth_version":7,"extra":true}`)},
		{name: "invalid utf8 key", body: append([]byte(`{"nickname":"Alice","expected_auth_version":7,"`), []byte{0xff, '"', ':', '1', '}'}...)},
		{name: "invalid utf8 value", body: append([]byte(`{"nickname":"`), append([]byte{0xff}, []byte(`","expected_auth_version":7}`)...)...)},
		{name: "lone high surrogate", body: []byte(`{"nickname":"\ud800","expected_auth_version":7}`)},
		{name: "lone low surrogate", body: []byte(`{"nickname":"\udc00","expected_auth_version":7}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeAdminUserEdit(bytes.NewReader(test.body))
			if !errors.Is(err, ErrAdminUserEditInvalidBody) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDecodeAdminUserEditRejectsMissingAndForbiddenFields(t *testing.T) {
	tests := map[string]string{
		"missing nickname":     `{"expected_auth_version":7}`,
		"missing version":      `{"nickname":"Alice"}`,
		"guid":                 `{"nickname":"Alice","expected_auth_version":7,"guid":"1"}`,
		"username":             `{"nickname":"Alice","expected_auth_version":7,"username":"alice"}`,
		"role":                 `{"nickname":"Alice","expected_auth_version":7,"role":"admin"}`,
		"auth version":         `{"nickname":"Alice","expected_auth_version":7,"auth_version":7}`,
		"status":               `{"nickname":"Alice","expected_auth_version":7,"status":"active"}`,
		"password":             `{"nickname":"Alice","expected_auth_version":7,"password":"secret"}`,
		"group guid":           `{"nickname":"Alice","expected_auth_version":7,"group_guid":null}`,
		"plan type":            `{"nickname":"Alice","expected_auth_version":7,"plan_type":"free"}`,
		"allowed models":       `{"nickname":"Alice","expected_auth_version":7,"allowed_models":[]}`,
		"daily call limit":     `{"nickname":"Alice","expected_auth_version":7,"daily_call_limit":1}`,
		"permission overrides": `{"nickname":"Alice","expected_auth_version":7,"permission_overrides":[]}`,
		"amount":               `{"nickname":"Alice","expected_auth_version":7,"amount":0}`,
		"balance":              `{"nickname":"Alice","expected_auth_version":7,"balance":0}`,
		"amount balance":       `{"nickname":"Alice","expected_auth_version":7,"amount_balance":0}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeAdminUserEdit(strings.NewReader(body))
			if !errors.Is(err, ErrAdminUserEditInvalidBody) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDecodeAdminUserEditRejectsNicknameBoundaries(t *testing.T) {
	tests := map[string]string{
		"wrong type":       `1`,
		"boolean":          `true`,
		"object":           `{}`,
		"array":            `[]`,
		"empty":            `""`,
		"whitespace only":  `" \t\n "`,
		"sixty five runes": fmt.Sprintf("%q", strings.Repeat("界", 65)),
	}
	for name, nickname := range tests {
		t.Run(name, func(t *testing.T) {
			body := `{"nickname":` + nickname + `,"expected_auth_version":7}`
			_, err := DecodeAdminUserEdit(strings.NewReader(body))
			if !errors.Is(err, ErrAdminUserEditInvalidBody) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDecodeAdminUserEditRejectsInvalidVersions(t *testing.T) {
	tests := map[string]string{
		"null":           `null`,
		"string":         `"1"`,
		"zero":           `0`,
		"negative":       `-1`,
		"float":          `1.0`,
		"exponent":       `1e1`,
		"signed":         `+1`,
		"leading zero":   `01`,
		"int32 overflow": `2147483648`,
		"huge overflow":  `999999999999999999999999999999999999`,
		"lone minus":     `-`,
		"lone decimal":   `.1`,
		"lone exponent":  `1e`,
	}
	for name, version := range tests {
		t.Run(name, func(t *testing.T) {
			body := `{"nickname":"Alice","expected_auth_version":` + version + `}`
			_, err := DecodeAdminUserEdit(strings.NewReader(body))
			if !errors.Is(err, ErrAdminUserEditInvalidBody) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDecodeAdminUserEditEnforcesBodyLimit(t *testing.T) {
	valid := []byte(`{"nickname":"A","expected_auth_version":1}`)
	exact := append(append([]byte(nil), valid...), bytes.Repeat([]byte(" "), int(AdminUserEditBodyLimit)-len(valid))...)
	got, err := DecodeAdminUserEdit(bytes.NewReader(exact))
	if err != nil || got.Nickname == nil || *got.Nickname != "A" {
		t.Fatalf("exact limit decoded=%+v err=%v", got, err)
	}

	tooLarge := append(exact, ' ')
	if _, err := DecodeAdminUserEdit(bytes.NewReader(tooLarge)); !errors.Is(err, ErrAdminUserEditBodyTooLarge) {
		t.Fatalf("oversized error=%v", err)
	}
}

func TestDecodeAdminUserEditCountsUnicodeCodePoints(t *testing.T) {
	nickname := strings.Repeat("😀", 64)
	if utf8.RuneCountInString(nickname) != 64 {
		t.Fatal("bad test fixture")
	}
	body := fmt.Sprintf(`{"nickname":%q,"expected_auth_version":7}`, nickname)
	got, err := DecodeAdminUserEdit(strings.NewReader(body))
	if err != nil || got.Nickname == nil || *got.Nickname != nickname {
		t.Fatalf("nickname=%v err=%v", got.Nickname, err)
	}
}

func stringPointer(value string) *string { return &value }
