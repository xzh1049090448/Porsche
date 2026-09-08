package service

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ErrAdminUsersGroupUnsupported = errors.New("group filter unsupported")

type AdminUsersReadQuery struct {
	Page     int64
	PageSize int
	Q        string
	Role     string
	Status   string
	Sort     string
	Order    string
}

func ParseAdminUsersReadQuery(raw string) (AdminUsersReadQuery, error) {
	q := AdminUsersReadQuery{Page: 1, PageSize: 20, Sort: "guid", Order: "desc"}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return q, ErrAdminPermissionInvalid
	}
	for key, v := range values {
		if len(v) != 1 {
			return q, ErrAdminPermissionInvalid
		}
		switch key {
		case "page", "page_size", "q", "role", "status", "sort", "order":
		case "group":
			return q, ErrAdminUsersGroupUnsupported
		default:
			return q, ErrAdminPermissionInvalid
		}
	}
	if v, ok := values["page"]; ok {
		n, err := ParseAdminPermissionGUID(v[0])
		if err != nil || n > 2147483647 {
			return q, ErrAdminPermissionInvalid
		}
		q.Page = n
	}
	if v, ok := values["page_size"]; ok {
		switch v[0] {
		case "20":
			q.PageSize = 20
		case "50":
			q.PageSize = 50
		case "100":
			q.PageSize = 100
		default:
			return q, ErrAdminPermissionInvalid
		}
	}
	q.Q = strings.TrimSpace(values.Get("q"))
	if !utf8.ValidString(q.Q) || utf8.RuneCountInString(q.Q) > 128 {
		return q, ErrAdminPermissionInvalid
	}
	q.Role = values.Get("role")
	if _, ok := values["role"]; ok && q.Role != "user" && q.Role != "admin" && q.Role != "root" {
		return q, ErrAdminPermissionInvalid
	}
	q.Status = values.Get("status")
	if _, ok := values["status"]; ok && q.Status != "active" && q.Status != "disabled" && q.Status != "deleted" {
		return q, ErrAdminPermissionInvalid
	}
	if v, ok := values["sort"]; ok {
		q.Sort = v[0]
	}
	if v, ok := values["order"]; ok {
		q.Order = v[0]
	}
	if !validUsersSort(q.Sort) || (q.Order != "asc" && q.Order != "desc") {
		q.Sort = "guid"
		q.Order = "desc"
	}
	return q, nil
}
func validUsersSort(s string) bool {
	return s == "guid" || s == "username" || s == "created_at" || s == "last_login_at"
}
func (q AdminUsersReadQuery) Offset() int64 { return (q.Page - 1) * int64(q.PageSize) }
func (q AdminUsersReadQuery) LikePattern() string {
	return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(q.Q) + "%"
}
func (q AdminUsersReadQuery) ExactGUID() int64 { v, _ := ParseAdminPermissionGUID(q.Q); return v }
func (q AdminUsersReadQuery) valid() bool {
	return q.Page > 0 && q.Page <= 2147483647 && (q.PageSize == 20 || q.PageSize == 50 || q.PageSize == 100) && validUsersSort(q.Sort) && (q.Order == "asc" || q.Order == "desc") && (q.Role == "" || q.Role == "user" || q.Role == "admin" || q.Role == "root") && (q.Status == "" || q.Status == "active" || q.Status == "disabled" || q.Status == "deleted") && utf8.ValidString(q.Q) && utf8.RuneCountInString(q.Q) <= 128
}

// Legacy pagination keeps the original Atoi fallback but bounds valid integers.
func ParseLegacyUsersPagination(skipRaw, limitRaw string) (int64, int, error) {
	skip, err := strconv.Atoi(skipRaw)
	if errors.Is(err, strconv.ErrRange) {
		return 0, 0, ErrAdminPermissionInvalid
	}
	if err != nil {
		skip = 0
	}
	limit, err := strconv.Atoi(limitRaw)
	if errors.Is(err, strconv.ErrRange) {
		return 0, 0, ErrAdminPermissionInvalid
	}
	if err != nil {
		limit = 50
	}
	if skip < 0 || limit < 0 || limit > 100 {
		return 0, 0, ErrAdminPermissionInvalid
	}
	return int64(skip), limit, nil
}
