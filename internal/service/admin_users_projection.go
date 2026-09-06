package service

import (
	"github.com/porsche/ai-gateway-go/internal/models"
	"strconv"
	"time"
)

// UserReadDTO contains only the approved public user-list fields.
type UserReadDTO struct {
	GUID        string  `json:"guid"`
	Username    *string `json:"username"`
	Nickname    *string `json:"nickname"`
	Email       *string `json:"email"`
	Group       *string `json:"group"`
	PlanType    string  `json:"plan_type"`
	Role        string  `json:"role"`
	Status      string  `json:"status"`
	AuthVersion int     `json:"auth_version"`
	CreatedAt   string  `json:"created_at"`
	LastLoginAt *string `json:"last_login_at"`
}

func ProjectUserRead(user models.User) (*UserReadDTO, error) {
	if !validPermissionReadUser(user) || user.Role == models.UserRoleRoot || (user.PlanType != models.PlanFree && user.PlanType != models.PlanProfessional && user.PlanType != models.PlanEnterprise) {
		return nil, ErrAdminPermissionUnavailable
	}
	status := user.Status.String()
	if user.IsDeleted == 1 {
		status = "deleted"
	}
	out := &UserReadDTO{GUID: strconv.FormatInt(user.Guid, 10), Username: user.Username, Nickname: user.Nickname, PlanType: user.PlanType.String(), Role: user.Role.String(), Status: status, AuthVersion: user.AuthVersion, CreatedAt: time.UnixMilli(user.CreatedAt).UTC().Format(time.RFC3339Nano)}
	if user.LastLoginAt != nil {
		s := time.UnixMilli(*user.LastLoginAt).UTC().Format(time.RFC3339Nano)
		out.LastLoginAt = &s
	}
	return out, nil
}

// ProjectCreatedUserRead binds the normal user projection to the exact group
// row locked by the create transaction.
func ProjectCreatedUserRead(user models.User, group models.BusinessGroup) (*UserReadDTO, error) {
	if group.ID <= 0 || group.Guid <= 0 || group.ID != user.GroupID || group.Key == "" ||
		group.Status != models.BusinessGroupStatusActive || group.IsDeleted != 0 {
		return nil, ErrAdminPermissionUnavailable
	}
	out, err := ProjectUserRead(user)
	if err != nil {
		return nil, err
	}
	key := group.Key
	out.Group = &key
	return out, nil
}

type AdminUsersReadPage struct {
	Items    []UserReadDTO `json:"items"`
	Total    int64         `json:"total"`
	Page     int64         `json:"page"`
	PageSize int           `json:"page_size"`
}
