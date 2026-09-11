package dto

import (
	"bytes"
	"testing"
)

func TestDecodeAdminUserPasswordResetIssueStrictAndOwned(t *testing.T) {
	raw := []byte(`{"action":"users.reset_password","intent":{"target_guid":"123","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"rotation"},"current_password":"Current!Pass9"}`)
	request, err := DecodeAdminUserPasswordResetIssue(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if request.TargetGUID != 123 || request.ExpectedAuthVersion != 7 || request.Reason != "rotation" || string(request.NewPassword) != "Strong!Pass1" || string(request.CurrentPassword) != "Current!Pass9" {
		t.Fatalf("decoded=%+v", request)
	}
	request.ClearSecrets()
	if request.NewPassword != nil || request.CurrentPassword != nil {
		t.Fatal("secrets retained")
	}
}

func TestDecodeAdminUserPasswordResetExecuteRejectsAmbiguousBodies(t *testing.T) {
	for _, raw := range []string{
		`{"action":"reset_password","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"x","reason":"y"}`,
		`{"action":"reset_password","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"x","unknown":1}`,
		`{"action":"reset_password","expected_auth_version":0,"new_password":"Strong!Pass1","reason":"x"}`,
		`{"action":"reset_password","expected_auth_version":7,"new_password":"password","reason":"x"}`,
	} {
		if request, err := DecodeAdminUserPasswordResetExecute(bytes.NewBufferString(raw)); err == nil {
			request.ClearSecrets()
			t.Fatalf("accepted %s", raw)
		}
	}
}
