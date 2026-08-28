package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
)

type AuthService struct {
	settings *config.Settings
	sms      *SMSService
	db       *gorm.DB
	sessions *SessionService
}

func NewAuthService(settings *config.Settings, sms *SMSService, db *gorm.DB) *AuthService {
	return &AuthService{settings: settings, sms: sms, db: db}
}

// SetSessionService wires the already-constructed revocable session service
// into username authentication without creating a second service instance.
func (a *AuthService) SetSessionService(sessions *SessionService) {
	a.sessions = sessions
}

// NormalizeUsername trims and validates the immutable username identifier.
// The character set intentionally matches Root bootstrap validation.
func NormalizeUsername(raw string) (string, error) {
	username := strings.TrimSpace(raw)
	if len(username) < 3 || len(username) > 20 {
		return "", errBadRequest("用户名长度必须为3到20个字符")
	}
	for _, character := range username {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '_' && character != '-' {
			return "", errBadRequest("用户名只能包含字母、数字、下划线或连字符")
		}
	}
	return username, nil
}

// ValidatePassword enforces the reviewed username-password registration
// contract before an Argon2id hash is generated.
func ValidatePassword(password string) error {
	if len([]rune(password)) < 8 || len([]rune(password)) > 20 {
		return errBadRequest("密码长度必须为8到20个字符")
	}
	switch strings.ToLower(strings.TrimSpace(password)) {
	case "password", "password123", "12345678", "qwerty123", "porsche", "porsche@2026":
		return errBadRequest("密码过于简单")
	}
	return nil
}

// RegisterUsername creates one ordinary username user. Username uniqueness is
// intentionally checked across tombstones and additionally enforced by MySQL's
// permanent unique index.
func (a *AuthService) RegisterUsername(ctx context.Context, rawUsername, password string, nickname *string) (*models.User, error) {
	if a == nil || a.db == nil || a.settings == nil || !a.settings.RegisterEnabled || !a.settings.PasswordRegisterEnabled {
		return nil, errForbidden("当前不允许注册")
	}
	if err := a.requireAuthRedis(ctx); err != nil {
		return nil, err
	}
	username, err := NormalizeUsername(rawUsername)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return nil, err
	}
	nick := username
	if nickname != nil && strings.TrimSpace(*nickname) != "" {
		nick = strings.TrimSpace(*nickname)
	}
	user := &models.User{
		AuditFields:   auditFields(nil),
		Username:      &username,
		PasswordHash:  &hash,
		Nickname:      &nick,
		PlanType:      models.PlanFree,
		Status:        models.UserStatusActive,
		Role:          models.UserRoleUser,
		AuthVersion:   1,
		AllowedModels: models.JSONSlice{},
	}
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing models.User
		if err := tx.Where("username = ?", username).First(&existing).Error; err == nil {
			return errConflict("用户名已注册")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		return tx.Create(&models.AuthAuditEvent{AuditFields: auditFields(&user.ID), UserID: &user.ID, EventType: models.AuthAuditEventRegistered, LoginMethod: loginMethodPointer(models.LoginMethodPassword)}).Error
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// LoginUsername validates an active, non-deleted username user then creates a
// revocable server session before issuing the corresponding Access JWT.
func (a *AuthService) LoginUsername(ctx context.Context, rawUsername, password string, input SessionCreateInput) (*models.User, *IssuedSession, string, error) {
	if a == nil || a.db == nil || a.settings == nil || !a.settings.PasswordLoginEnabled || a.sessions == nil {
		return nil, nil, "", errUnauthorized("用户名或密码错误")
	}
	username, err := NormalizeUsername(rawUsername)
	if err != nil {
		return nil, nil, "", errUnauthorized("用户名或密码错误")
	}
	redisStore, err := a.authRedis()
	if err != nil {
		return nil, nil, "", errUnauthorized("用户名或密码错误")
	}
	if err := redisStore.CheckLoginAllowed(ctx, username, input.IP); err != nil {
		return nil, nil, "", errUnauthorized("用户名或密码错误")
	}
	var user models.User
	if err := a.db.WithContext(ctx).Where("username = ? AND is_deleted = 0", username).First(&user).Error; err != nil || user.PasswordHash == nil || !security.VerifyPassword(password, *user.PasswordHash) || !user.Status.IsActive() {
		if recordErr := redisStore.RecordLoginFailure(ctx, username, input.IP); recordErr != nil {
			return nil, nil, "", errUnauthorized("用户名或密码错误")
		}
		return nil, nil, "", errUnauthorized("用户名或密码错误")
	}
	if err := redisStore.ClearLoginFailures(ctx, username, input.IP); err != nil {
		return nil, nil, "", errUnauthorized("用户名或密码错误")
	}
	issued, err := a.sessions.Create(ctx, &user, input)
	if err != nil {
		return nil, nil, "", err
	}
	token, err := a.makeToken(&user, issued.Session)
	if err != nil {
		return nil, nil, "", err
	}
	return &user, issued, token, nil
}

func (a *AuthService) authRedis() (*AuthRedis, error) {
	if a == nil || a.sessions == nil || a.sessions.redis == nil {
		return nil, errors.New("Redis authentication store is unavailable")
	}
	return a.sessions.redis, nil
}

func (a *AuthService) requireAuthRedis(ctx context.Context) error {
	store, err := a.authRedis()
	if err != nil {
		return err
	}
	return store.CheckAvailable(ctx)
}

// BootstrapRoot creates the deployment-configured Root only when no active or
// tombstoned Root has ever existed. Clearing the in-memory bootstrap values
// makes the one-time input ineffective after successful creation.
func (a *AuthService) BootstrapRoot(ctx context.Context) (*models.User, error) {
	if a == nil || a.db == nil || a.settings == nil {
		return nil, errors.New("authentication service is unavailable")
	}
	if a.settings.RootBootstrapUsername == "" || a.settings.RootBootstrapPassword == "" {
		return nil, nil
	}
	username, err := NormalizeUsername(a.settings.RootBootstrapUsername)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(a.settings.RootBootstrapPassword); err != nil {
		return nil, err
	}
	hash, err := security.HashPassword(a.settings.RootBootstrapPassword)
	if err != nil {
		return nil, err
	}
	root := &models.User{AuditFields: auditFields(nil), Username: &username, PasswordHash: &hash, Nickname: &username, PlanType: models.PlanFree, Status: models.UserStatusActive, Role: models.UserRoleRoot, AuthVersion: 1, AllowedModels: models.JSONSlice{}}
	// GET_LOCK is connection-scoped, so Connection keeps the root-existence
	// check and insert on one MySQL connection across concurrent replicas.
	err = a.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		var locked int
		if err := conn.Raw("SELECT GET_LOCK(?, ?)", "porsche_auth_root_bootstrap", 10).Scan(&locked).Error; err != nil {
			return err
		}
		if locked != 1 {
			return errors.New("root bootstrap lock unavailable")
		}
		defer func() { _ = conn.Exec("SELECT RELEASE_LOCK(?)", "porsche_auth_root_bootstrap").Error }()
		return conn.Transaction(func(tx *gorm.DB) error {
			var count int64
			// A Root tombstone is permanent bootstrap history. Looking across
			// deleted rows prevents a deleted Root from being silently replaced.
			if err := tx.Model(&models.User{}).Where("role = ?", models.UserRoleRoot).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
			if err := tx.Create(root).Error; err != nil {
				return err
			}
			return tx.Create(&models.AuthAuditEvent{AuditFields: auditFields(&root.ID), UserID: &root.ID, EventType: models.AuthAuditEventRegistered, LoginMethod: loginMethodPointer(models.LoginMethodPassword)}).Error
		})
	})
	if err != nil {
		return nil, err
	}
	a.settings.RootBootstrapUsername = ""
	a.settings.RootBootstrapPassword = ""
	if root.ID == 0 {
		return nil, nil
	}
	return root, nil
}

// CanManageUser enforces the role hierarchy for state-changing administrator
// operations. Management is strictly downward and Root accounts are never
// mutable through normal administrator workflows.
func CanManageUser(actor, target *models.User) error {
	if actor == nil || target == nil || actor.IsDeleted != 0 || target.IsDeleted != 0 {
		return errForbidden("无权限管理该用户")
	}
	if target.Role == models.UserRoleRoot || actor.Role <= target.Role {
		return errForbidden("无权限管理该用户")
	}
	return nil
}

func loginMethodPointer(method models.LoginMethod) *models.LoginMethod { return &method }

func (a *AuthService) makeToken(user *models.User, session *models.Session) (string, error) {
	if user == nil || session == nil || user.Guid <= 0 || session.SID == "" || session.SessionVersion <= 0 || user.AuthVersion <= 0 || user.Role < models.UserRoleUser {
		return "", errors.New("invalid access token subject")
	}
	minutes := a.settings.SessionAccessMinutes
	if minutes <= 0 {
		minutes = a.settings.JWTExpireMinutes
	}
	return security.CreateAccessToken(strconv.FormatInt(user.Guid, 10), a.settings.JWTSecretKey, minutes, map[string]interface{}{
		"sid":  session.SID,
		"sv":   session.SessionVersion,
		"av":   user.AuthVersion,
		"role": int(user.Role),
	})
}

func HashIDCard(idCard string) string {
	sum := sha256.Sum256([]byte(idCard))
	return hex.EncodeToString(sum[:])
}

type SMSService struct {
	settings  *config.Settings
	mu        sync.Mutex
	codes     map[string]codeEntry
	sendPhone map[string]windowCount
	sendIP    map[string]windowCount
}

type codeEntry struct {
	code    string
	expires time.Time
}

type windowCount struct {
	count   int
	expires time.Time
}

func NewSMSService(settings *config.Settings) *SMSService {
	return &SMSService{
		settings:  settings,
		codes:     make(map[string]codeEntry),
		sendPhone: make(map[string]windowCount),
		sendIP:    make(map[string]windowCount),
	}
}

func (s *SMSService) CheckSendAllowed(phone, ip string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.incrLocked(s.sendPhone, "phone:"+phone, now) > s.settings.SMSSendLimitPerPhone {
		return errTooMany("验证码发送过于频繁，请稍后再试")
	}
	if s.incrLocked(s.sendIP, "ip:"+ip, now) > s.settings.SMSSendLimitPerIP {
		return errTooMany("请求过于频繁，请稍后再试")
	}
	return nil
}

func (s *SMSService) incrLocked(store map[string]windowCount, key string, now time.Time) int {
	w := store[key]
	if now.After(w.expires) {
		w = windowCount{count: 0, expires: now.Add(time.Hour)}
	}
	w.count++
	store[key] = w
	return w.count
}

func (s *SMSService) SendCode(phone string) string {
	code := fmt.Sprintf("%06d", rand.Intn(900000)+100000)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[phone] = codeEntry{code: code, expires: time.Now().Add(5 * time.Minute)}
	return code
}

func (s *SMSService) VerifyCode(phone, code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.codes[strings.TrimSpace(phone)]
	if !ok || time.Now().After(entry.expires) {
		return false
	}
	if strings.TrimSpace(code) != entry.code {
		return false
	}
	delete(s.codes, phone)
	return true
}
