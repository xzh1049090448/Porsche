package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const validAdminUserCreateJSON = `{"username":"  alice  ","nickname":"  Alice  ","password":"Ex4mple!Pass1","role":"admin","group_guid":"123456789012345678","plan_type":"professional","permission_overrides":[{"capability":"users.sessions.read","effect":"allow"},{"capability":"users.plan.change","effect":"deny"}]}`

func TestDecodeAdminUserCreateNormalizesAndOwnsSecrets(t *testing.T) {
	source := []byte(validAdminUserCreateJSON)
	got, err := DecodeAdminUserCreate(bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "alice" || got.Nickname == nil || *got.Nickname != "Alice" || got.Role != models.UserRoleAdmin || got.GroupGUID == nil || *got.GroupGUID != 123456789012345678 || got.PlanType != models.PlanProfessional {
		t.Fatalf("decoded create=%+v", got)
	}
	if string(got.Password) != "Ex4mple!Pass1" || !bytes.Equal(got.Password, []byte("Ex4mple!Pass1")) {
		t.Fatal("password changed")
	}
	clear(source)
	if string(got.Password) != "Ex4mple!Pass1" {
		t.Fatal("password aliases source")
	}
	want := []actionsecurity.PermissionOverrideIntent{{Capability: "users.plan.change", Effect: 3}, {Capability: "users.sessions.read", Effect: 2}}
	if fmt.Sprint(got.PermissionOverrides) != fmt.Sprint(want) {
		t.Fatalf("overrides=%#v want=%#v", got.PermissionOverrides, want)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		rendered := fmt.Sprintf(verb, got)
		if !strings.Contains(rendered, "<redacted>") || strings.Contains(rendered, "Ex4mple!Pass1") {
			t.Fatalf("format %s exposed password: %s", verb, rendered)
		}
	}
	password := got.Password
	got.ClearSecrets()
	if got.Password != nil {
		t.Fatal("ClearSecrets left password attached")
	}
	for _, value := range password {
		if value != 0 {
			t.Fatal("ClearSecrets did not zero password")
		}
	}
}

func TestDecodeAdminUserCreateRejectsStrictJSONAndPublicInternalFields(t *testing.T) {
	cases := map[string]string{
		"unknown":           validAdminUserCreateJSON[:len(validAdminUserCreateJSON)-1] + `,"extra":1}`,
		"duplicate":         strings.Replace(validAdminUserCreateJSON, `"username":"  alice  "`, `"username":"alice","username":"alice"`, 1),
		"case collision":    strings.Replace(validAdminUserCreateJSON, `"username"`, `"Username"`, 1),
		"escaped collision": strings.Replace(validAdminUserCreateJSON, `"username"`, `"\\u0075sername"`, 1),
		"nested duplicate":  strings.Replace(validAdminUserCreateJSON, `"capability":"users.sessions.read"`, `"capability":"users.sessions.read","capability":"users.read"`, 1),
		"nested unknown":    strings.Replace(validAdminUserCreateJSON, `"effect":"allow"`, `"effect":"allow","extra":true`, 1),
		"trailing":          validAdminUserCreateJSON + `{}`,
		"amount":            validAdminUserCreateJSON[:len(validAdminUserCreateJSON)-1] + `,"amount":1}`,
		"balance":           validAdminUserCreateJSON[:len(validAdminUserCreateJSON)-1] + `,"balance":1}`,
		"status":            validAdminUserCreateJSON[:len(validAdminUserCreateJSON)-1] + `,"status":"active"}`,
		"auth version":      validAdminUserCreateJSON[:len(validAdminUserCreateJSON)-1] + `,"auth_version":1}`,
		"allowed models":    validAdminUserCreateJSON[:len(validAdminUserCreateJSON)-1] + `,"allowed_models":[]}`,
		"daily limit":       validAdminUserCreateJSON[:len(validAdminUserCreateJSON)-1] + `,"daily_call_limit":100}`,
		"internal id":       validAdminUserCreateJSON[:len(validAdminUserCreateJSON)-1] + `,"internal_id":1}`,
		"invalid utf8":      string(append([]byte(`{"username":"alice","password":"Ex4mple!Pass1","role":"user","nickname":"`), append([]byte{0xff}, []byte(`"}`)...)...)),
		"lone surrogate":    `{"username":"alice","password":"Ex4mple!Pass1","role":"user","nickname":"\\ud800"}`,
	}
	cases["lone surrogate"] = `{"username":"alice","password":"Ex4mple!Pass1","role":"user","nickname":"` + string([]byte{'\\', 'u', 'd', '8', '0', '0'}) + `"}`
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeAdminUserCreate(strings.NewReader(body))
			got.ClearSecrets()
			if !errors.Is(err, ErrAdminUserCreateInvalidBody) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := DecodeAdminUserCreate(strings.NewReader(strings.Repeat(" ", int(AdminUserCreateBodyLimit+1)))); !errors.Is(err, ErrAdminUserCreateBodyTooLarge) {
		t.Fatalf("large body error=%v", err)
	}
}

func TestDecodeAdminUserCreateDefaultsAndPasswordEscapes(t *testing.T) {
	password := "a\"b\\c/d\b\f\n\r\t世😀"
	body, err := json.Marshal(map[string]string{"username": "alice", "password": password, "role": "user"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAdminUserCreate(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer got.ClearSecrets()
	if got.Nickname != nil || got.GroupGUID != nil || got.PlanType != models.PlanFree || got.PermissionOverrides == nil || len(got.PermissionOverrides) != 0 {
		t.Fatalf("defaults=%+v", got)
	}
	if string(got.Password) != password {
		t.Fatalf("password=%q want=%q", got.Password, password)
	}
}

func TestDecodeAdminUserCreateValidatesFields(t *testing.T) {
	cases := map[string]string{
		"missing username":       `{"password":"Ex4mple!Pass1","role":"user"}`,
		"bad username":           `{"username":" a b ","password":"Ex4mple!Pass1","role":"user"}`,
		"missing password":       `{"username":"alice","role":"user"}`,
		"short password":         `{"username":"alice","password":"short","role":"user"}`,
		"weak password":          `{"username":"alice","password":"password","role":"user"}`,
		"password not string":    `{"username":"alice","password":1,"role":"user"}`,
		"empty nickname":         `{"username":"alice","nickname":"   ","password":"Ex4mple!Pass1","role":"user"}`,
		"long nickname":          `{"username":"alice","nickname":"` + strings.Repeat("界", 65) + `","password":"Ex4mple!Pass1","role":"user"}`,
		"root role":              `{"username":"alice","password":"Ex4mple!Pass1","role":"root"}`,
		"unknown role":           `{"username":"alice","password":"Ex4mple!Pass1","role":"operator"}`,
		"bad group number":       `{"username":"alice","password":"Ex4mple!Pass1","role":"user","group_guid":1}`,
		"bad group zero":         `{"username":"alice","password":"Ex4mple!Pass1","role":"user","group_guid":"0"}`,
		"bad group leading zero": `{"username":"alice","password":"Ex4mple!Pass1","role":"user","group_guid":"01"}`,
		"bad group overflow":     `{"username":"alice","password":"Ex4mple!Pass1","role":"user","group_guid":"9223372036854775808"}`,
		"bad plan":               `{"username":"alice","password":"Ex4mple!Pass1","role":"user","plan_type":"vip"}`,
		"bad override effect":    `{"username":"alice","password":"Ex4mple!Pass1","role":"admin","permission_overrides":[{"capability":"users.read","effect":"inherit"}]}`,
		"empty capability":       `{"username":"alice","password":"Ex4mple!Pass1","role":"admin","permission_overrides":[{"capability":"","effect":"allow"}]}`,
		"unknown capability":     `{"username":"alice","password":"Ex4mple!Pass1","role":"admin","permission_overrides":[{"capability":"no.such","effect":"allow"}]}`,
		"unavailable capability": `{"username":"alice","password":"Ex4mple!Pass1","role":"admin","permission_overrides":[{"capability":"users.quota.adjust","effect":"allow"}]}`,
		"root capability":        `{"username":"alice","password":"Ex4mple!Pass1","role":"admin","permission_overrides":[{"capability":"users.promote","effect":"allow"}]}`,
		"user override":          `{"username":"alice","password":"Ex4mple!Pass1","role":"user","permission_overrides":[{"capability":"users.read","effect":"allow"}]}`,
		"duplicate override":     `{"username":"alice","password":"Ex4mple!Pass1","role":"admin","permission_overrides":[{"capability":"users.read","effect":"allow"},{"capability":"users.read","effect":"deny"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeAdminUserCreate(strings.NewReader(body))
			got.ClearSecrets()
			if !errors.Is(err, ErrAdminUserCreateInvalidBody) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDecodeAdminUserCreateVerificationLifecycle(t *testing.T) {
	body := `{"action":"users.create_admin","intent":` + validAdminUserCreateJSON + `,"current_password":"Current!Pass1"}`
	got, err := DecodeAdminUserCreateVerification(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "users.create_admin" || got.Intent.Role != models.UserRoleAdmin || string(got.CurrentPassword) != "Current!Pass1" {
		t.Fatalf("verification=%+v", got)
	}
	initial, current := got.Intent.Password, got.CurrentPassword
	got.ClearSecrets()
	if got.Intent.Password != nil || got.CurrentPassword != nil {
		t.Fatal("verification ClearSecrets did not nil secrets")
	}
	for _, secret := range [][]byte{initial, current} {
		for _, value := range secret {
			if value != 0 {
				t.Fatal("verification secret was not zeroed")
			}
		}
	}

	for _, body := range []string{
		strings.Replace(body, `users.create_admin`, `users.create`, 1),
		strings.Replace(body, `"role":"admin"`, `"role":"user"`, 1),
		strings.Replace(body, `"current_password":"Current!Pass1"`, `"current_password":null`, 1),
		body + `{}`,
	} {
		got, err := DecodeAdminUserCreateVerification(strings.NewReader(body))
		got.ClearSecrets()
		if err == nil {
			t.Fatal("accepted invalid verification")
		}
	}
}

func TestAdminUserCreateStringAndFormatRedactAllPasswords(t *testing.T) {
	request, err := DecodeAdminUserCreateVerification(strings.NewReader(`{"action":"users.create_admin","intent":{"username":"alice","password":"Ex4mple!Pass1","role":"admin"},"current_password":"Current!Pass1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer request.ClearSecrets()
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		rendered := fmt.Sprintf(verb, request)
		if !strings.Contains(rendered, "<redacted>") || strings.Contains(rendered, "Ex4mple!Pass1") || strings.Contains(rendered, "Current!Pass1") {
			t.Fatalf("format %s exposed secret: %s", verb, rendered)
		}
	}
}

func TestAdminUserCreatePasswordStillMatchesServiceValidation(t *testing.T) {
	got, err := DecodeAdminUserCreate(strings.NewReader(`{"username":"alice","password":"Ex4mple!Pass1","role":"user"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer got.ClearSecrets()
	if !utf8.Valid(got.Password) || service.ValidatePassword(string(got.Password)) != nil {
		t.Fatal("decoder accepted a password service rejects")
	}
}
