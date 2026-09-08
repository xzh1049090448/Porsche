package dto

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeAdminUserEntitlementExactBodies(t *testing.T) {
	group, err := DecodeAdminUserGroupChange(strings.NewReader(`{"group_guid":"123","reason":" move ","expected_auth_version":7}`))
	if err != nil || group.GroupGUID != 123 || group.Reason != "move" || group.ExpectedAuthVersion != 7 {
		t.Fatalf("group=%+v err=%v", group, err)
	}
	plan, err := DecodeAdminUserPlanChange(strings.NewReader(`{"plan_type":"professional","reason":" grant ","expected_auth_version":8}`))
	if err != nil || plan.PlanType != "professional" || plan.Reason != "grant" || plan.ExpectedAuthVersion != 8 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestDecodeAdminUserEntitlementRejectsAmbiguousBodies(t *testing.T) {
	cases := []string{
		`{}`, `null`, `{"plan_type":"pro","reason":"x","expected_auth_version":1}`,
		`{"plan_type":"free","reason":"x","expected_auth_version":1,"extra":1}`,
		`{"plan_type":"free","PLAN_TYPE":"enterprise","reason":"x","expected_auth_version":1}`,
		`{"group_guid":"01","reason":"x","expected_auth_version":1}`,
		`{"group_guid":"1","reason":"","expected_auth_version":1}`,
		`{"group_guid":"1","reason":"x","expected_auth_version":2147483648}`,
	}
	for _, body := range cases {
		if strings.Contains(body, "group_guid") {
			if _, err := DecodeAdminUserGroupChange(strings.NewReader(body)); !errors.Is(err, ErrAdminUserEntitlementInvalidBody) {
				t.Fatalf("group %q err=%v", body, err)
			}
		} else if _, err := DecodeAdminUserPlanChange(strings.NewReader(body)); !errors.Is(err, ErrAdminUserEntitlementInvalidBody) {
			t.Fatalf("plan %q err=%v", body, err)
		}
	}
}
