package service

import (
	"encoding/json"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"reflect"
	"strings"
	"testing"
)

func TestAdminUsersReadQuery(t *testing.T) {
	for _, raw := range []string{"page=0", "page=01", "page=2147483648", "page=%2B1", "page_size=10", "page_size=020", "page=1&page=2", "wat=1", "q=%FF", "q=%zz", "q=" + strings.Repeat("界", 129), "status=no", "role=no", "group=", "page=1;limit=3"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseAdminUsersReadQuery(raw); err == nil {
				t.Fatal("accepted invalid query")
			}
		})
	}
	q, err := ParseAdminUsersReadQuery("q=%20a%25_!%20&sort=username&order=asc&page=2147483647&page_size=100")
	if err != nil || q.Offset() != 214748364600 || q.LikePattern() != "%a!%!_!!%" || q.Sort != "username" || q.Order != "asc" {
		t.Fatalf("query=%+v err=%v", q, err)
	}
	q, err = ParseAdminUsersReadQuery("sort=username&order=no")
	if err != nil || q.Sort != "guid" || q.Order != "desc" {
		t.Fatal(q, err)
	}
	q, err = ParseAdminUsersReadQuery("")
	if err != nil || q.Page != 1 || q.PageSize != 20 || q.Status != "" {
		t.Fatal(q, err)
	}
	for _, raw := range []string{"0", "01", "+1", "9223372036854775808"} {
		q, _ = ParseAdminUsersReadQuery("q=" + strings.ReplaceAll(raw, "+", "%2B"))
		if q.ExactGUID() != 0 {
			t.Fatal(raw)
		}
	}
	q, _ = ParseAdminUsersReadQuery("q=9223372036854775807")
	if q.ExactGUID() != 9223372036854775807 {
		t.Fatal(q)
	}
}
func TestAdminUsersReadProjection(t *testing.T) {
	last := int64(1700000000123)
	u := models.User{ID: 33, AuditFields: models.AuditFields{Guid: 9223372036854775807, CreatedAt: 0, IsDeleted: 1}, Role: models.UserRoleUser, Status: models.UserStatusDisabled, PlanType: models.PlanFree, LastLoginAt: &last, AuthVersion: 1}
	v, err := ProjectUserRead(u)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	var m map[string]interface{}
	json.Unmarshal(b, &m)
	if len(m) != 10 || m["guid"] != "9223372036854775807" || m["status"] != "deleted" || m["created_at"] != "1970-01-01T00:00:00Z" || m["last_login_at"] != "2023-11-14T22:13:20.123Z" || m["email"] != nil || m["group"] != nil {
		t.Fatal(string(b))
	}
	u.LastLoginAt = nil
	v, _ = ProjectUserRead(u)
	if v.LastLoginAt != nil {
		t.Fatal("invented last login")
	}
	u.PlanType = 99
	if _, err = ProjectUserRead(u); err == nil {
		t.Fatal("unknown plan")
	}
}

func TestAuthProjectionConstructor(t *testing.T) {
	user := models.User{ID: 1, AuditFields: models.AuditFields{Guid: 2}, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 1}
	e, _ := authz.NewEvaluator(accountForRead(user), nil)
	p, err := projectAuthPermissions(e, 0)
	if err != nil || p.PermissionsVersion != "0" || p.AdminPermissions == nil || len(p.AdminPermissions) != 0 {
		t.Fatal(p, err)
	}
	user.Role = models.UserRoleRoot
	e, _ = authz.NewEvaluator(accountForRead(user), nil)
	p, err = projectAuthPermissions(e, 8)
	if err != nil || p.PermissionsVersion != "8" || len(p.AdminPermissions) == 0 {
		t.Fatal(p, err)
	}
	if _, err = projectAuthPermissions(e, -1); err == nil {
		t.Fatal("negative version")
	}
}

func TestAdminUsersReadLegacyPagination(t *testing.T) {
	for _, tc := range []struct {
		s, l  string
		skip  int64
		limit int
		bad   bool
	}{
		{"", "", 0, 50, false}, {"x", "no", 0, 50, false}, {"2", "0", 2, 0, false}, {"-1", "50", 0, 0, true}, {"0", "101", 0, 0, true}, {"9223372036854775808", "50", 0, 0, true}, {"0", "999999999999999999999", 0, 0, true},
	} {
		skip, limit, err := ParseLegacyUsersPagination(tc.s, tc.l)
		if (err != nil) != tc.bad || !tc.bad && (skip != tc.skip || limit != tc.limit) {
			t.Fatalf("%+v got %d/%d/%v", tc, skip, limit, err)
		}
	}
}

func TestAdminUsersListStatementReusesPredicateAndArguments(t *testing.T) {
	root := models.User{ID: 41, AuditFields: models.AuditFields{Guid: 42}, Role: models.UserRoleRoot}
	queries := []AdminUsersReadQuery{
		{Page: 1, PageSize: 20, Sort: "guid", Order: "asc"},
		{Page: 2, PageSize: 50, Status: "active", Sort: "username", Order: "desc"},
		{Page: 3, PageSize: 100, Status: "disabled", Role: "user", Sort: "created_at", Order: "asc"},
		{Page: 4, PageSize: 20, Status: "deleted", Role: "admin", Q: "a%_!", Sort: "last_login_at", Order: "desc"},
		{Page: 5, PageSize: 50, Role: "root", Q: "9223372036854775807", Sort: "guid", Order: "desc"},
	}
	for _, q := range queries {
		statement, args := adminUsersListStatement(root, q)
		where, whereArgs := usersReadWhere(root, q)
		if strings.Count(statement, where) != 2 {
			t.Fatalf("predicate not reused exactly twice for %+v: %s", q, statement)
		}
		if strings.Count(statement, "IGNORE INDEX FOR JOIN (idx_users_active_updated)") != 1 {
			t.Fatalf("count-local ignore index missing or leaked for %+v: %s", q, statement)
		}
		if !strings.Contains(statement, "filtered AS (SELECT ") || !strings.Contains(statement, "paged AS (SELECT * FROM filtered ORDER BY ") {
			t.Fatalf("filtered/paged source changed for %+v: %s", q, statement)
		}
		if len(args) != 2*len(whereArgs)+2 || !reflect.DeepEqual(args[:len(whereArgs)], whereArgs) || !reflect.DeepEqual(args[len(whereArgs):2*len(whereArgs)], whereArgs) {
			t.Fatalf("predicate args not copied in order for %+v: %#v want two copies of %#v", q, args, whereArgs)
		}
		if args[len(args)-2] != q.PageSize || args[len(args)-1] != q.Offset() {
			t.Fatalf("limit/offset bind order changed for %+v: %#v", q, args)
		}
		order := q.Sort + " " + q.Order
		if q.Sort != "guid" {
			order += ", guid " + q.Order
		}
		if !strings.Contains(statement, "ORDER BY "+order+" LIMIT ? OFFSET ?") {
			t.Fatalf("paged ordering changed for %+v: %s", q, statement)
		}
	}
}
