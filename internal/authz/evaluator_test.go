package authz

import (
	"reflect"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func account(id int64, role models.UserRole) Account {
	return Account{ID: id, GUID: id + 1000, Role: role, Status: models.UserStatusActive}
}

func evaluator(t *testing.T, role models.UserRole, overrides ...Override) *Evaluator {
	t.Helper()
	e, err := NewEvaluator(account(1, role), overrides)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestCatalogAndBaseline(t *testing.T) {
	want := map[string]bool{
		"users.read": true, "users.create": true, "users.edit": true, "users.enable": true, "users.disable": true,
		"users.reset_password": false, "users.sessions.read": false, "users.sessions.revoke": false,
		"users.plan.change": false, "users.group.change": false, "users.quota.adjust": false,
		"users.delete": false, "users.deleted.read": false, "users.promote": false, "users.demote": false,
		"users.permissions.write": false, "users.audit.read": true, "groups.read": true, "groups.write": false,
		"public_content.read": false, "public_content.edit": false, "public_content.preview": false,
		"public_content.publish": false, "public_content.rollback": false,
	}
	rootOnly := map[string]bool{"users.promote": true, "users.demote": true, "users.permissions.write": true, "groups.write": true}
	catalog := Catalog()
	if len(catalog) != 24 {
		t.Fatalf("catalog size %d", len(catalog))
	}
	admin := evaluator(t, models.UserRoleAdmin)
	root := evaluator(t, models.UserRoleRoot)
	seen := map[string]bool{}
	for _, d := range catalog {
		expected, ok := want[d.Name]
		if !ok || seen[d.Name] {
			t.Fatalf("unexpected or duplicate %q", d.Name)
		}
		seen[d.Name] = true
		unavailable := d.Name == "users.quota.adjust"
		if d.AdminDefault != expected || d.RootOnly != rootOnly[d.Name] || d.Unavailable != unavailable || d.Grantable != (!unavailable && !rootOnly[d.Name]) {
			t.Fatalf("incorrect definition %+v", d)
		}
		if (admin.capability(d.Name) == Allowed) != expected {
			t.Fatalf("admin baseline %s", d.Name)
		}
		if (root.capability(d.Name) == Allowed) == unavailable {
			t.Fatalf("root availability %s", d.Name)
		}
	}
	if len(evaluator(t, models.UserRoleUser).CapabilityNames()) != 0 {
		t.Fatal("ordinary user acquired management capabilities")
	}
}

func TestOverridePrecedenceAndValidation(t *testing.T) {
	target := account(2, models.UserRoleUser)
	for _, tc := range []struct {
		effect Effect
		want   Decision
	}{{Inherit, Denied}, {Allow, Allowed}, {Deny, Denied}} {
		e := evaluator(t, models.UserRoleAdmin, Override{"users.reset_password", tc.effect})
		if got := e.User("users.reset_password", target); got != tc.want {
			t.Fatalf("%s: %v", tc.effect, got)
		}
	}
	e := evaluator(t, models.UserRoleAdmin, Override{"users.read", Deny})
	if e.User("users.read", target) != Denied {
		t.Fatal("explicit deny did not override default allow")
	}
	for _, d := range Catalog() {
		_, err := NewEvaluator(account(1, models.UserRoleAdmin), []Override{{d.Name, Allow}})
		if (err == nil) != d.Grantable {
			t.Fatalf("grantability %s: %v", d.Name, err)
		}
	}
	invalid := [][]Override{
		{{"unknown", Allow}}, {{"users.read", Effect("invalid")}},
		{{"users.read", Allow}, {"users.read", Deny}}, {{"users.promote", Inherit}}, {{"users.quota.adjust", Deny}},
	}
	for _, rules := range invalid {
		if _, err := NewEvaluator(account(1, models.UserRoleAdmin), rules); err == nil {
			t.Fatalf("accepted invalid overrides %v", rules)
		}
	}
	for _, role := range []models.UserRole{models.UserRoleUser, models.UserRoleRoot} {
		if _, err := NewEvaluator(account(1, role), []Override{{"users.read", Allow}}); err == nil {
			t.Fatalf("accepted overrides for %v", role)
		}
	}
}

func TestBadActorsFailClosed(t *testing.T) {
	base := account(1, models.UserRoleAdmin)
	bad := []Account{{}}
	for _, mutate := range []func(*Account){
		func(a *Account) { a.ID = 0 }, func(a *Account) { a.GUID = -1 }, func(a *Account) { a.Role = models.UserRole(11) },
		func(a *Account) { a.Status = models.UserStatusDisabled }, func(a *Account) { a.Status = models.UserStatus(99) },
		func(a *Account) { a.IsDeleted = 1 }, func(a *Account) { a.IsDeleted = -1 },
	} {
		a := base
		mutate(&a)
		bad = append(bad, a)
	}
	for _, a := range bad {
		if _, err := NewEvaluator(a, nil); err == nil {
			t.Fatalf("accepted actor %+v", a)
		}
	}
	var nilEvaluator *Evaluator
	var zeroEvaluator Evaluator
	if nilEvaluator.User("users.read", account(2, models.UserRoleUser)) != Denied || zeroEvaluator.Resource("groups.read") != Denied || len(nilEvaluator.CapabilityNames()) != 0 {
		t.Fatal("zero/nil evaluator is not closed")
	}
}

func TestUserHardBoundariesAndInvalidTargets(t *testing.T) {
	admin := evaluator(t, models.UserRoleAdmin)
	root := evaluator(t, models.UserRoleRoot)
	for _, target := range []Account{
		account(1, models.UserRoleUser), account(2, models.UserRoleAdmin), account(3, models.UserRoleRoot), {},
		{ID: 2, GUID: 0, Role: models.UserRoleUser, Status: models.UserStatusActive},
		{ID: 2, GUID: 1002, Role: models.UserRole(11), Status: models.UserStatusActive},
		{ID: 2, GUID: 1002, Role: models.UserRoleUser, Status: models.UserStatus(99)},
	} {
		for _, capability := range []string{"users.read", "users.reset_password"} {
			if admin.User(capability, target) != Hidden {
				t.Fatalf("%s exposed target %+v", capability, target)
			}
		}
	}
	selfGUID := account(2, models.UserRoleUser)
	selfGUID.GUID = 1001
	if root.User("users.read", selfGUID) != Hidden || root.User("users.read", account(3, models.UserRoleRoot)) != Hidden {
		t.Fatal("root/self bypass")
	}
	target := account(2, models.UserRoleUser)
	target.Status = models.UserStatusDisabled
	if admin.User("users.read", target) != Allowed {
		t.Fatal("disabled target cannot be inspected")
	}
	if root.User("users.promote", account(2, models.UserRoleAdmin)) != Denied || root.User("users.demote", account(2, models.UserRoleUser)) != Denied || root.User("users.permissions.write", account(2, models.UserRoleUser)) != Denied || root.User("users.promote", account(2, models.UserRoleUser)) != Allowed || root.User("users.demote", account(2, models.UserRoleAdmin)) != Allowed || root.User("users.permissions.write", account(2, models.UserRoleAdmin)) != Allowed {
		t.Fatal("role transition boundary")
	}
}

func TestTombstonesAreReadOnly(t *testing.T) {
	target := account(2, models.UserRoleUser)
	target.IsDeleted = 1
	admin := evaluator(t, models.UserRoleAdmin)
	if admin.User("users.read", target) != Hidden || admin.User("users.audit.read", target) != Hidden {
		t.Fatal("tombstone visible without deleted.read")
	}
	reader := evaluator(t, models.UserRoleAdmin, Override{"users.deleted.read", Allow})
	for _, capability := range []string{"users.read", "users.audit.read", "users.deleted.read"} {
		if reader.User(capability, target) != Allowed {
			t.Fatalf("tombstone read denied %s", capability)
		}
	}
	root := evaluator(t, models.UserRoleRoot)
	for _, d := range Catalog() {
		if d.scopes&userScope != 0 && d.Name != "users.read" && d.Name != "users.audit.read" && d.Name != "users.deleted.read" && root.User(d.Name, target) == Allowed {
			t.Fatalf("tombstone mutation allowed %s", d.Name)
		}
	}
	if root.User("users.deleted.read", account(2, models.UserRoleUser)) != Hidden {
		t.Fatal("active account treated as tombstone")
	}
}

func TestEntrypointsCannotBypassTargets(t *testing.T) {
	root := evaluator(t, models.UserRoleRoot)
	admin := evaluator(t, models.UserRoleAdmin)
	for _, d := range Catalog() {
		if d.scopes&resourceScope == 0 && root.Resource(d.Name) == Allowed {
			t.Fatalf("targetless bypass %s", d.Name)
		}
		if d.scopes&collectionScope == 0 && root.Collection(d.Name, false) == Allowed {
			t.Fatalf("collection bypass %s", d.Name)
		}
	}
	if root.User("public_content.edit", account(2, models.UserRoleUser)) != Denied || root.Resource("unknown") != Denied || root.Resource("public_content.write") != Denied || root.User("users.quota.adjust", account(2, models.UserRoleUser)) != Denied || admin.Collection("users.read", true) != Denied || admin.Collection("users.read", false) != Allowed || root.Collection("users.audit.read", true) != Allowed {
		t.Fatal("entrypoint or availability boundary")
	}
	if admin.Resource("groups.read") != Allowed || admin.Resource("groups.write") != Denied || root.Resource("groups.write") != Allowed {
		t.Fatal("group boundary")
	}
	content := evaluator(t, models.UserRoleAdmin, Override{"public_content.edit", Allow}, Override{"users.read", Deny})
	if content.Resource("public_content.edit") != Allowed || content.Collection("users.read", false) != Denied {
		t.Fatal("content permission granted user management")
	}
	for _, role := range []models.UserRole{0, 11, models.UserRoleRoot} {
		if root.Create(role) != Denied {
			t.Fatalf("created forbidden role %v", role)
		}
	}
	if admin.Create(models.UserRoleUser) != Allowed || admin.Create(models.UserRoleAdmin) != Denied || root.Create(models.UserRoleAdmin) != Allowed || evaluator(t, models.UserRoleUser).Create(models.UserRoleUser) != Denied {
		t.Fatal("create role boundary")
	}
}

func TestInputsAndProjectionAreIsolated(t *testing.T) {
	rules := []Override{{"users.read", Deny}}
	e, err := NewEvaluator(account(1, models.UserRoleAdmin), rules)
	if err != nil {
		t.Fatal(err)
	}
	rules[0].Effect = Allow
	if e.Collection("users.read", false) != Denied {
		t.Fatal("retained caller-owned overrides")
	}
	catalog := Catalog()
	catalog[0].Name = "changed"
	if Catalog()[0].Name == "changed" {
		t.Fatal("catalog exposed mutable state")
	}
	root := evaluator(t, models.UserRoleRoot)
	before := root.CapabilityNames()
	after := root.CapabilityNames()
	if !reflect.DeepEqual(before, after) || len(before) != 23 {
		t.Fatal("projection unstable or unavailable capability included")
	}
	after[0] = "changed"
	if !reflect.DeepEqual(root.CapabilityNames(), before) {
		t.Fatal("projection exposed mutable state")
	}
	if root.User("users.read", account(1, models.UserRoleUser)) != Hidden {
		t.Fatal("projection bypassed self boundary")
	}
}

func TestConcurrentEvaluation(t *testing.T) {
	e := evaluator(t, models.UserRoleRoot)
	done := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		go func() {
			ok := true
			for j := 0; j < 100; j++ {
				ok = ok && e.User("users.read", account(2, models.UserRoleUser)) == Allowed
				ok = ok && len(e.CapabilityNames()) == 23
			}
			done <- ok
		}()
	}
	for i := 0; i < 16; i++ {
		if !<-done {
			t.Fatal("concurrent decision mismatch")
		}
	}
}
