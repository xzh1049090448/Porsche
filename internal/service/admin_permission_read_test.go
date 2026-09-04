package service

import (
	"context"
	"errors"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"testing"
)

func TestAdminPermissionCatalogProjectionAndGUID(t *testing.T) {
	c := AdminPermissionCatalogProjection()
	if c.CatalogVersion != 1 || len(c.OverrideEffects) != 3 || len(c.Capabilities) != 24 {
		t.Fatalf("catalog=%+v", c)
	}
	if c.Capabilities[0].Name != "users.read" || c.Capabilities[10].Available || !c.Capabilities[0].AdminDefault {
		t.Fatal("stable catalog projection mismatch")
	}
	for _, raw := range []string{"", "0", "01", "+1", "-1", " 1", "1 ", "x", "9223372036854775808"} {
		if _, err := ParseAdminPermissionGUID(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if got, err := ParseAdminPermissionGUID("123"); err != nil || got != 123 {
		t.Fatalf("guid=%d err=%v", got, err)
	}
}

func TestAdminPermissionDetailProjection(t *testing.T) {
	for _, status := range []models.UserStatus{models.UserStatusActive, models.UserStatusDisabled} {
		target := models.User{ID: 2, AuditFields: models.AuditFields{Guid: 123}, Role: models.UserRoleAdmin, Status: status, AuthVersion: 1}
		got, err := adminPermissionDetailProjection(target, 7, []authz.Override{{Capability: "users.read", Effect: authz.Deny}, {Capability: "users.reset_password", Effect: authz.Allow}})
		if err != nil || got == nil || len(got.Capabilities) != 24 || got.PermissionsVersion != "7" {
			t.Fatalf("projection: %v", err)
		}
		for i, c := range got.Capabilities {
			want := (i < 5 || i == 16 || i == 17 || i == 5) && i != 0
			if c.PolicyEffective != want || c.Effective != (status == models.UserStatusActive && want) {
				t.Fatalf("capability %d: %+v", i, c)
			}
		}
	}
	for _, rules := range [][]authz.Override{{{Capability: "users.promote", Effect: authz.Allow}}, {{Capability: "users.quota.adjust", Effect: authz.Allow}}, {{Capability: "users.read", Effect: "bad"}}, {{Capability: "users.read", Effect: authz.Allow}, {Capability: "users.read", Effect: authz.Deny}}} {
		got, err := adminPermissionDetailProjection(models.User{ID: 2, AuditFields: models.AuditFields{Guid: 123}, Role: models.UserRoleAdmin, Status: models.UserStatusDisabled, AuthVersion: 1}, 0, rules)
		if got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
			t.Fatal("corrupt projection accepted")
		}
	}
}

func TestAdminPermissionReadRejectsUnavailableInputs(t *testing.T) {
	actor := AdminPermissionReadActor{UserID: 1, AuthVersion: 1, SessionSID: "session", SessionVersion: 1}
	for _, root := range []*gorm.DB{nil, {}} {
		s := NewAdminPermissionReadService(root, nil)
		if got, err := s.Catalog(context.Background(), actor); got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
			t.Fatal("accepted unavailable catalog")
		}
		if got, err := s.Detail(context.Background(), actor, 123); got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
			t.Fatal("accepted unavailable detail")
		}
	}
}

func TestAdminPermissionFreshAuthenticationClassification(t *testing.T) {
	actor := AdminPermissionReadActor{UserID: 1, AuthVersion: 2, SessionSID: "sid", SessionVersion: 3}
	user := models.User{ID: 1, AuditFields: models.AuditFields{Guid: 11}, Role: models.UserRoleRoot, Status: models.UserStatusActive, AuthVersion: 2}
	for _, tc := range []struct {
		name   string
		mutate func(*models.User)
		want   error
	}{
		{"valid", func(*models.User) {}, nil},
		{"disabled", func(u *models.User) { u.Status = models.UserStatusDisabled }, ErrAdminPermissionUnauthenticated},
		{"deleted", func(u *models.User) { u.IsDeleted = 1 }, ErrAdminPermissionUnauthenticated},
		{"stale", func(u *models.User) { u.AuthVersion++ }, ErrAdminPermissionUnauthenticated},
		{"unknownrole", func(u *models.User) { u.Role = 999 }, ErrAdminPermissionUnavailable},
		{"unknownstatus", func(u *models.User) { u.Status = 999 }, ErrAdminPermissionUnavailable},
		{"invalidversion", func(u *models.User) { u.AuthVersion = 0 }, ErrAdminPermissionUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := user
			tc.mutate(&u)
			if err := validateAdminReadActor(u, actor); !errors.Is(err, tc.want) {
				t.Fatalf("classification=%v", err)
			}
		})
	}
	session := models.Session{ID: 1, AuditFields: models.AuditFields{Guid: 12}, UserID: 1, SID: "sid", SessionVersion: 3, ExpiresAt: 1001}
	for _, tc := range []struct {
		name   string
		mutate func(*models.Session)
		want   error
	}{
		{"valid", func(*models.Session) {}, nil},
		{"expired", func(s *models.Session) { s.ExpiresAt = 1000 }, ErrAdminPermissionUnauthenticated},
		{"revoked", func(s *models.Session) { n := int64(1); s.RevokedAt = &n }, ErrAdminPermissionUnauthenticated},
		{"deleted", func(s *models.Session) { s.IsDeleted = 1 }, ErrAdminPermissionUnauthenticated},
		{"stale", func(s *models.Session) { s.SessionVersion++ }, ErrAdminPermissionUnauthenticated},
		{"foreign", func(s *models.Session) { s.UserID++ }, ErrAdminPermissionUnauthenticated},
		{"invalidversion", func(s *models.Session) { s.SessionVersion = 0 }, ErrAdminPermissionUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := session
			tc.mutate(&s)
			if err := validateAdminReadSession(s, actor, 1000); !errors.Is(err, tc.want) {
				t.Fatalf("classification=%v", err)
			}
		})
	}
}
