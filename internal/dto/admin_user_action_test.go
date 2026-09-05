package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

const validUserDeleteIssueJSON = `{"action":"users.delete","intent":{"target_guid":"123456789012345678","expected_auth_version":7,"reason":"duplicate account"},"current_password":"example-only-not-a-secret"}`

func TestDecodeUserDeleteIssue(t *testing.T) {
	source := []byte(validUserDeleteIssueJSON)
	got, err := DecodeUserDeleteIssue(bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "users.delete" || got.TargetGUID != 123456789012345678 || got.ExpectedVersion != 7 || got.Reason != "duplicate account" || string(got.Password) != "example-only-not-a-secret" {
		t.Fatalf("decoded issue differs: action=%q guid=%d version=%d reason=%q password_length=%d", got.Action, got.TargetGUID, got.ExpectedVersion, got.Reason, len(got.Password))
	}
	clear(source)
	if string(got.Password) != "example-only-not-a-secret" {
		t.Fatal("password aliases caller source")
	}
	rendered := fmt.Sprint(got)
	if strings.Contains(rendered, "example-only-not-a-secret") || strings.Contains(rendered, "101 120 97 109 112 108 101") {
		t.Fatalf("normalized issue string exposed password material: %s", rendered)
	}
	clear(got.Password)
	for _, value := range got.Password {
		if value != 0 {
			t.Fatal("caller cannot clear password")
		}
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("example-only-not-a-secret")) {
		t.Fatal("normalized issue JSON exposed password")
	}
}

func TestDecodeUserDeleteExecute(t *testing.T) {
	got, err := DecodeUserDeleteExecute(strings.NewReader(`{"action":"delete","expected_auth_version":7,"reason":"  duplicate account  "}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpectedVersion != 7 || got.Reason != "duplicate account" {
		t.Fatalf("decoded execute differs: %+v", got)
	}
}

func TestUserDeleteStrictJSON(t *testing.T) {
	issueCases := map[string]string{
		"empty":                     ``,
		"array":                     `[]`,
		"unknown top":               `{"action":"users.delete","intent":{"target_guid":"1","expected_auth_version":1,"reason":"x"},"current_password":"p","extra":1}`,
		"unknown nested":            `{"action":"users.delete","intent":{"target_guid":"1","expected_auth_version":1,"reason":"x","extra":1},"current_password":"p"}`,
		"duplicate top":             `{"action":"users.delete","action":"users.delete","intent":{"target_guid":"1","expected_auth_version":1,"reason":"x"},"current_password":"p"}`,
		"duplicate nested":          `{"action":"users.delete","intent":{"target_guid":"1","target_guid":"2","expected_auth_version":1,"reason":"x"},"current_password":"p"}`,
		"trailing JSON":             validUserDeleteIssueJSON + `{}`,
		"malformed":                 `{"action":`,
		"wrong action":              strings.Replace(validUserDeleteIssueJSON, `users.delete`, `delete`, 1),
		"missing password":          `{"action":"users.delete","intent":{"target_guid":"1","expected_auth_version":1,"reason":"x"}}`,
		"empty password":            strings.Replace(validUserDeleteIssueJSON, `example-only-not-a-secret`, ``, 1),
		"wrong password type":       strings.Replace(validUserDeleteIssueJSON, `"example-only-not-a-secret"`, `7`, 1),
		"missing intent":            `{"action":"users.delete","current_password":"p"}`,
		"wrong intent type":         `{"action":"users.delete","intent":[],"current_password":"p"}`,
		"guid number":               strings.Replace(validUserDeleteIssueJSON, `"123456789012345678"`, `123456789012345678`, 1),
		"guid sign":                 strings.Replace(validUserDeleteIssueJSON, `123456789012345678`, `+1`, 1),
		"guid whitespace":           strings.Replace(validUserDeleteIssueJSON, `123456789012345678`, ` 1`, 1),
		"guid leading zero":         strings.Replace(validUserDeleteIssueJSON, `123456789012345678`, `01`, 1),
		"guid zero":                 strings.Replace(validUserDeleteIssueJSON, `123456789012345678`, `0`, 1),
		"guid overflow":             strings.Replace(validUserDeleteIssueJSON, `123456789012345678`, `9223372036854775808`, 1),
		"version string":            strings.Replace(validUserDeleteIssueJSON, `"expected_auth_version":7`, `"expected_auth_version":"7"`, 1),
		"version fractional":        strings.Replace(validUserDeleteIssueJSON, `"expected_auth_version":7`, `"expected_auth_version":7.0`, 1),
		"version exponent":          strings.Replace(validUserDeleteIssueJSON, `"expected_auth_version":7`, `"expected_auth_version":7e0`, 1),
		"version zero":              strings.Replace(validUserDeleteIssueJSON, `"expected_auth_version":7`, `"expected_auth_version":0`, 1),
		"version overflow":          strings.Replace(validUserDeleteIssueJSON, `"expected_auth_version":7`, `"expected_auth_version":2147483648`, 1),
		"empty trimmed reason":      strings.Replace(validUserDeleteIssueJSON, `duplicate account`, `  `, 1),
		"reason over codepoint max": strings.Replace(validUserDeleteIssueJSON, `duplicate account`, strings.Repeat("界", 201), 1),
		"lone high surrogate":       strings.Replace(validUserDeleteIssueJSON, `duplicate account`, `\ud800`, 1),
		"lone low surrogate":        strings.Replace(validUserDeleteIssueJSON, `duplicate account`, `\udc00`, 1),
	}
	for name, body := range issueCases {
		t.Run("issue "+name, func(t *testing.T) {
			if got, err := DecodeUserDeleteIssue(strings.NewReader(body)); err == nil {
				clear(got.Password)
				t.Fatal("accepted invalid issue")
			}
		})
	}

	executeCases := map[string]string{
		"unknown":           `{"action":"delete","expected_auth_version":7,"reason":"x","extra":1}`,
		"duplicate":         `{"action":"delete","action":"delete","expected_auth_version":7,"reason":"x"}`,
		"trailing":          `{"action":"delete","expected_auth_version":7,"reason":"x"}{}`,
		"wrong action":      `{"action":"users.delete","expected_auth_version":7,"reason":"x"}`,
		"version string":    `{"action":"delete","expected_auth_version":"7","reason":"x"}`,
		"version zero":      `{"action":"delete","expected_auth_version":0,"reason":"x"}`,
		"reason whitespace": `{"action":"delete","expected_auth_version":7,"reason":" "}`,
	}
	for name, body := range executeCases {
		t.Run("execute "+name, func(t *testing.T) {
			if _, err := DecodeUserDeleteExecute(strings.NewReader(body)); err == nil {
				t.Fatal("accepted invalid execute")
			}
		})
	}
}

func TestUserDeleteBodyLimitAndUnicode(t *testing.T) {
	tooLarge := strings.Repeat(" ", int(UserDeleteBodyLimit+1))
	if _, err := DecodeUserDeleteIssue(strings.NewReader(tooLarge)); !errors.Is(err, ErrUserDeleteBodyTooLarge) {
		t.Fatalf("oversize error=%v", err)
	}
	invalidUTF8 := append([]byte(`{"action":"delete","expected_auth_version":7,"reason":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	if utf8.Valid(invalidUTF8) {
		t.Fatal("fixture is valid UTF-8")
	}
	if _, err := DecodeUserDeleteExecute(bytes.NewReader(invalidUTF8)); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	reason := `  keep  internal\tspace  `
	got, err := DecodeUserDeleteExecute(strings.NewReader(`{"action":"delete","expected_auth_version":2147483647,"reason":"` + reason + `"}`))
	if err != nil || got.ExpectedVersion != 2147483647 || got.Reason != "keep  internal\tspace" {
		t.Fatalf("valid boundary differs: %+v err=%v", got, err)
	}
}

func TestUserDeleteResponseDTOExactJSON(t *testing.T) {
	finishedAt := int64(1790000000000)
	failureCode := "operation_failed"
	cases := []struct {
		value any
		want  string
	}{
		{UserDeleteIssueResponse{Ticket: "ticket", ExpiresAt: 1790000300000}, `{"ticket":"ticket","expires_at":1790000300000}`},
		{UserDeleteExecuteResponse{OperationRef: "operation", User: UserDeleteResponseUser{GUID: "123", Status: "deleted"}}, `{"operation_ref":"operation","user":{"guid":"123","status":"deleted"}}`},
		{UserDeleteQueryResponse{OperationRef: "operation", Scope: "users.delete", Status: "processing"}, `{"operation_ref":"operation","scope":"users.delete","status":"processing","finished_at":null,"failure_code":null}`},
		{UserDeleteQueryResponse{OperationRef: "operation", Scope: "users.delete", Status: "failed", FinishedAt: &finishedAt, FailureCode: &failureCode}, `{"operation_ref":"operation","scope":"users.delete","status":"failed","finished_at":1790000000000,"failure_code":"operation_failed"}`},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.value)
		if err != nil || string(got) != tc.want {
			t.Fatalf("response JSON=%s err=%v want=%s", got, err, tc.want)
		}
	}
}
