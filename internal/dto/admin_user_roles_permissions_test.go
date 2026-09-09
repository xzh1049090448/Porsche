package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
)

const (
	validPromoteIssue    = `{"action":"users.promote","intent":{"target_guid":"123456789012345678","expected_auth_version":7,"expected_permissions_version":0,"catalog_version":1,"overrides":[{"capability":"users.sessions.read","effect":"allow"},{"capability":"users.delete","effect":"deny"},{"capability":"users.read","effect":"inherit"}],"reason":"  grant duties  "},"current_password":"current-secret"}`
	validDemoteIssue     = `{"action":"users.demote","intent":{"target_guid":"123456789012345678","expected_auth_version":7,"expected_permissions_version":3,"catalog_version":1,"reason":" remove duties "},"current_password":"current-secret"}`
	validPermissionIssue = `{"action":"users.permissions.write","intent":{"target_guid":"123456789012345678","expected_auth_version":7,"expected_permissions_version":3,"catalog_version":1,"overrides":[{"capability":"users.sessions.read","effect":"allow"}],"reason":" rotate "},"current_password":"current-secret"}`
)

func TestDecodePromoteIssueAndExecute(t *testing.T) {
	issue, err := DecodePromoteIssue(strings.NewReader(validPromoteIssue))
	if err != nil {
		t.Fatal(err)
	}
	want := actionsecurity.PromoteIntent{TargetGUID: 123456789012345678, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.delete", Effect: 3}, {Capability: "users.sessions.read", Effect: 2}}, Reason: "grant duties"}
	if issue.Action != "users.promote" || !reflect.DeepEqual(issue.Intent, want) || string(issue.CurrentPassword) != "current-secret" {
		t.Fatalf("issue=%#v", issue)
	}
	assertRolePasswordOwnedAndRedacted(t, &issue.CurrentPassword, issue)
	execute, err := DecodePromoteExecute(strings.NewReader(`{"action":"promote","expected_auth_version":7,"expected_permissions_version":0,"catalog_version":1,"overrides":[],"reason":" grant "}`))
	if err != nil || execute.Action != "promote" || execute.ExpectedAuthVersion != 7 || execute.ExpectedPermissionsVersion != 0 || execute.CatalogVersion != 1 || execute.Reason != "grant" {
		t.Fatalf("execute=%#v err=%v", execute, err)
	}
	if got := execute.Intent(42); got.TargetGUID != 42 {
		t.Fatalf("intent=%#v", got)
	}
}

func TestDecodeDemoteIssueAndExecute(t *testing.T) {
	issue, err := DecodeDemoteIssue(strings.NewReader(validDemoteIssue))
	if err != nil {
		t.Fatal(err)
	}
	if issue.Action != "users.demote" || issue.Intent.TargetGUID != 123456789012345678 || issue.Intent.ExpectedPermissionsVersion != 3 || issue.Intent.Reason != "remove duties" {
		t.Fatalf("issue=%#v", issue)
	}
	assertRolePasswordOwnedAndRedacted(t, &issue.CurrentPassword, issue)
	execute, err := DecodeDemoteExecute(strings.NewReader(`{"action":"demote","expected_auth_version":7,"expected_permissions_version":3,"catalog_version":1,"reason":" remove "}`))
	if err != nil || execute.Action != "demote" || execute.Intent(42).TargetGUID != 42 {
		t.Fatalf("execute=%#v err=%v", execute, err)
	}
}

func TestDecodePermissionIssueAndPatch(t *testing.T) {
	issue, err := DecodePermissionsWriteIssue(strings.NewReader(validPermissionIssue))
	if err != nil {
		t.Fatal(err)
	}
	if issue.Action != "users.permissions.write" || issue.Intent.TargetGUID != 123456789012345678 || len(issue.Intent.Overrides) != 1 {
		t.Fatalf("issue=%#v", issue)
	}
	assertRolePasswordOwnedAndRedacted(t, &issue.CurrentPassword, issue)
	patch, err := DecodePermissionsWriteExecute(strings.NewReader(`{"expected_auth_version":7,"expected_permissions_version":3,"catalog_version":1,"overrides":[{"capability":"users.read","effect":"deny"}],"reason":" rotate "}`))
	if err != nil || patch.ExpectedAuthVersion != 7 || patch.Intent(42).TargetGUID != 42 || len(patch.Overrides) != 1 {
		t.Fatalf("patch=%#v err=%v", patch, err)
	}
}

func TestDecodePromoteDemotePermissionRejectMalformedBodies(t *testing.T) {
	decoders := []struct {
		name   string
		decode func(io.Reader) error
		valid  string
	}{
		{"promote issue", func(r io.Reader) error { _, e := DecodePromoteIssue(r); return e }, validPromoteIssue},
		{"demote issue", func(r io.Reader) error { _, e := DecodeDemoteIssue(r); return e }, validDemoteIssue},
		{"permission issue", func(r io.Reader) error { _, e := DecodePermissionsWriteIssue(r); return e }, validPermissionIssue},
	}
	for _, decoder := range decoders {
		t.Run(decoder.name, func(t *testing.T) {
			cases := []io.Reader{nil, errorReader{}, bytes.NewReader([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), strings.NewReader(decoder.valid + ` {}`), strings.NewReader(strings.Repeat(" ", int(RolePermissionBodyLimit+1)))}
			for i, body := range cases {
				err := decoder.decode(body)
				if err == nil {
					t.Fatalf("case %d accepted", i)
				}
				if i == len(cases)-1 && !errors.Is(err, ErrRolePermissionBodyTooLarge) {
					t.Fatalf("oversize=%v", err)
				}
			}
		})
	}
	mutations := []string{
		strings.Replace(validPromoteIssue, `"action"`, `"\u0061ction"`, 1),
		strings.Replace(validPromoteIssue, `"action":"users.promote"`, `"action":"users.promote","Action":"users.promote"`, 1),
		strings.Replace(validPromoteIssue, `"reason":`, `"reason":"x","reason":`, 1),
		strings.Replace(validPromoteIssue, `"reason":`, `"unknown":1,"reason":`, 1),
		strings.Replace(validPromoteIssue, `,"reason":"  grant duties  "`, ``, 1),
	}
	for _, body := range mutations {
		if _, err := DecodePromoteIssue(strings.NewReader(body)); !errors.Is(err, ErrRolePermissionInvalidBody) {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
}

func TestDecodePromoteDemotePermissionValidatesVersionsGUIDAndReason(t *testing.T) {
	for _, guid := range []string{"", "0", "01", "+1", " 1", "9223372036854775808"} {
		body := strings.Replace(validPromoteIssue, "123456789012345678", guid, 1)
		if _, err := DecodePromoteIssue(strings.NewReader(body)); err == nil {
			t.Fatalf("guid %q accepted", guid)
		}
	}
	for _, replacement := range []string{"0", "-1", "2147483648", "1.0", `"1"`} {
		body := strings.Replace(validPromoteIssue, `"expected_auth_version":7`, `"expected_auth_version":`+replacement, 1)
		if _, err := DecodePromoteIssue(strings.NewReader(body)); err == nil {
			t.Fatalf("auth %q accepted", replacement)
		}
	}
	if _, err := DecodePromoteIssue(strings.NewReader(strings.Replace(validPromoteIssue, `"expected_permissions_version":0`, `"expected_permissions_version":9223372036854775807`, 1))); err != nil {
		t.Fatal(err)
	}
	for _, valid := range []string{validDemoteIssue, validPermissionIssue} {
		if strings.Contains(valid, `"expected_permissions_version":3`) {
			body := strings.Replace(valid, `"expected_permissions_version":3`, `"expected_permissions_version":0`, 1)
			if strings.Contains(valid, "users.demote") {
				if _, err := DecodeDemoteIssue(strings.NewReader(body)); err == nil {
					t.Fatal("demote accepted zero permission version")
				}
			} else if _, err := DecodePermissionsWriteIssue(strings.NewReader(body)); err == nil {
				t.Fatal("permissions accepted zero permission version")
			}
		}
	}
	for _, version := range []string{"0", "2147483648", "-1"} {
		body := strings.Replace(validPromoteIssue, `"catalog_version":1`, `"catalog_version":`+version, 1)
		if _, err := DecodePromoteIssue(strings.NewReader(body)); err == nil {
			t.Fatalf("catalog %s accepted", version)
		}
	}
	for _, reason := range []string{"  ", strings.Repeat("界", 201), `\ud800`, `\udc00`} {
		body := strings.Replace(validPromoteIssue, `  grant duties  `, reason, 1)
		if _, err := DecodePromoteIssue(strings.NewReader(body)); err == nil {
			t.Fatalf("reason accepted: %q", reason)
		}
	}
	if _, err := DecodePromoteExecute(strings.NewReader(`{"action":"demote","expected_auth_version":7,"expected_permissions_version":0,"catalog_version":1,"overrides":[],"reason":"x"}`)); !errors.Is(err, ErrRolePermissionInactiveAction) {
		t.Fatalf("action=%v", err)
	}
}

func TestDecodePromoteDemotePermissionValidatesOverrides(t *testing.T) {
	bad := []string{
		`[{"capability":"users.read"}]`, `[{"capability":"users.read","effect":"allow","x":1}]`,
		`[{"capability":"users.read","effect":"allow"},{"capability":"users.read","effect":"deny"}]`,
		`[{"capability":"users.read","effect":"bad"}]`, `[{"capability":"unknown","effect":"deny"}]`,
		`[{"capability":"users.quota.adjust","effect":"deny"}]`, `[{"capability":"users.promote","effect":"allow"}]`,
		`[{"capability":"users.read","effect":"allow"},{"Capability":"users.read","effect":"deny"}]`,
	}
	for _, overrides := range bad {
		body := replaceJSONArray(validPromoteIssue, "overrides", overrides)
		if _, err := DecodePromoteIssue(strings.NewReader(body)); err == nil {
			t.Fatalf("overrides accepted: %s", overrides)
		}
	}
	if _, err := DecodeDemoteIssue(strings.NewReader(strings.Replace(validDemoteIssue, `,"reason"`, `,"overrides":[],"reason"`, 1))); err == nil {
		t.Fatal("demote accepted overrides")
	}
}

func TestRolePermissionStableResponseShapes(t *testing.T) {
	result := RolePermissionResult{OperationRef: "op_x", TargetGUID: "42", ResultingAuthVersion: 8, ResultingPermissionsVersion: 4, ResultingRole: "admin"}
	query := RolePermissionQuery{OperationRef: "op_x", Scope: "users.promote", Status: "succeeded", FinishedAt: ptr(int64(1)), TargetGUID: ptr("42"), ResultingAuthVersion: ptr(8), ResultingPermissionsVersion: ptr(int64(4)), ResultingRole: ptr("admin")}
	for value, keys := range map[any][]string{result: {"operation_ref", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role"}, query: {"operation_ref", "scope", "status", "finished_at", "failure_code", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role"}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if json.Unmarshal(data, &object) != nil || len(object) != len(keys) {
			t.Fatalf("json=%s", data)
		}
		for _, key := range keys {
			if _, ok := object[key]; !ok {
				t.Fatalf("missing %s in %s", key, data)
			}
		}
	}
}

func assertRolePasswordOwnedAndRedacted(t *testing.T, password *[]byte, value any) {
	t.Helper()
	for _, rendered := range []string{fmt.Sprint(value), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value)} {
		if strings.Contains(rendered, "current-secret") || !strings.Contains(rendered, "<redacted>") {
			t.Fatalf("leak: %s", rendered)
		}
	}
	data, _ := json.Marshal(value)
	if bytes.Contains(data, []byte("current-secret")) {
		t.Fatalf("JSON leak: %s", data)
	}
	original := *password
	original[0] = 'X'
	if (*password)[0] != 'X' {
		t.Fatal("password is not caller-owned")
	}
	clear(*password)
	for _, b := range original {
		if b != 0 {
			t.Fatal("password not clearable")
		}
	}
}

func replaceJSONArray(body, key, replacement string) string {
	start := strings.Index(body, `"`+key+`":`)
	if start < 0 {
		return body
	}
	start += len(key) + 3
	end := start
	depth := 0
	for ; end < len(body); end++ {
		if body[end] == '[' {
			depth++
		}
		if body[end] == ']' {
			depth--
			if depth == 0 {
				end++
				break
			}
		}
	}
	return body[:start] + replacement + body[end:]
}
func ptr[T any](value T) *T { return &value }

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

var _ = math.MaxInt32
