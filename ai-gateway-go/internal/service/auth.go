package service

import (
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
}

func NewAuthService(settings *config.Settings, sms *SMSService, db *gorm.DB) *AuthService {
	return &AuthService{settings: settings, sms: sms, db: db}
}

func (a *AuthService) Register(phone, code, password string, nickname *string) (*models.User, string, error) {
	if !a.sms.VerifyCode(phone, code) {
		return nil, "", errBadRequest("验证码无效或已过期")
	}
	var existing models.User
	if err := a.db.Where("phone = ?", phone).First(&existing).Error; err == nil {
		return nil, "", errConflict("手机号已注册")
	}
	hash, _ := security.HashPassword(password)
	nick := fmt.Sprintf("用户%s", phone[len(phone)-4:])
	if nickname != nil && *nickname != "" {
		nick = *nickname
	}
	user := models.User{
		Phone:        phone,
		PasswordHash: &hash,
		Nickname:     &nick,
		PlanType:     models.PlanFree,
		Status:       models.UserStatusActive,
	}
	if err := a.db.Create(&user).Error; err != nil {
		return nil, "", err
	}
	token, err := a.makeToken(&user)
	return &user, token, err
}

func (a *AuthService) LoginPassword(phone, password string) (*models.User, string, error) {
	phone = strings.TrimSpace(phone)
	if a.settings.FixedLoginEnabled {
		if phone != strings.TrimSpace(a.settings.FixedLoginPhone) || password != a.settings.FixedLoginPassword {
			return nil, "", errUnauthorized("手机号或密码错误")
		}
		user, err := a.getOrCreateFixedUser(phone)
		if err != nil {
			return nil, "", err
		}
		if err := ensureActive(user); err != nil {
			return nil, "", err
		}
		token, err := a.makeToken(user)
		return user, token, err
	}

	var user models.User
	if err := a.db.Where("phone = ?", phone).First(&user).Error; err != nil {
		return nil, "", errUnauthorized("手机号或密码错误")
	}
	if user.PasswordHash == nil || !security.VerifyPassword(password, *user.PasswordHash) {
		return nil, "", errUnauthorized("手机号或密码错误")
	}
	if err := ensureActive(&user); err != nil {
		return nil, "", err
	}
	token, err := a.makeToken(&user)
	return &user, token, err
}

func (a *AuthService) LoginCode(phone, code string) (*models.User, string, error) {
	if !a.sms.VerifyCode(phone, code) {
		return nil, "", errBadRequest("验证码无效或已过期")
	}
	var user models.User
	err := a.db.Where("phone = ?", phone).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		nick := fmt.Sprintf("用户%s", phone[len(phone)-4:])
		user = models.User{
			Phone:    phone,
			Nickname: &nick,
			PlanType: models.PlanFree,
			Status:   models.UserStatusActive,
		}
		if err := a.db.Create(&user).Error; err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", err
	}
	if err := ensureActive(&user); err != nil {
		return nil, "", err
	}
	token, err := a.makeToken(&user)
	return &user, token, err
}

func (a *AuthService) getOrCreateFixedUser(phone string) (*models.User, error) {
	var user models.User
	err := a.db.Where("phone = ?", phone).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		hash, _ := security.HashPassword(a.settings.FixedLoginPassword)
		nick := "测试用户"
		user = models.User{
			Phone:        phone,
			PasswordHash: &hash,
			Nickname:     &nick,
			PlanType:     models.PlanFree,
			Status:       models.UserStatusActive,
		}
		return &user, a.db.Create(&user).Error
	}
	if err != nil {
		return nil, err
	}
	if user.PasswordHash == nil {
		hash, _ := security.HashPassword(a.settings.FixedLoginPassword)
		user.PasswordHash = &hash
		_ = a.db.Save(&user).Error
	}
	return &user, nil
}

func (a *AuthService) makeToken(user *models.User) (string, error) {
	return security.CreateAccessToken(strconv.FormatUint(uint64(user.ID), 10), a.settings.JWTSecretKey, a.settings.JWTExpireMinutes, map[string]interface{}{
		"plan": string(user.PlanType),
	})
}

func HashIDCard(idCard string) string {
	sum := sha256.Sum256([]byte(idCard))
	return hex.EncodeToString(sum[:])
}

func ensureActive(user *models.User) error {
	if user.Status != models.UserStatusActive {
		return errForbidden("账号已被禁用")
	}
	return nil
}

type SMSService struct {
	settings *config.Settings
	mu       sync.Mutex
	codes    map[string]codeEntry
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
