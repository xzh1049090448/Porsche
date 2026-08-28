package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SessionCreateInput contains only request metadata required to establish a
// server-side authentication session. It deliberately contains no credentials.
type SessionCreateInput struct {
	LoginMethod models.LoginMethod
	IP          string
	UserAgent   string
}

// IssuedSession contains the persisted session and the only plaintext refresh
// token copy. It must be sent directly to the HttpOnly cookie layer and never
// persisted or logged.
type IssuedSession struct {
	Session      *models.Session
	RefreshToken string
}

// SessionService owns MySQL-backed session lifecycle transitions. Redis is a
// mandatory security dependency: every Redis failure is returned to callers.
type SessionService struct {
	db       *gorm.DB
	redis    *AuthRedis
	settings *config.Settings
	now      func() int64
}

// NewSessionService creates a session service. A nil Redis dependency remains
// fail-closed: all authentication operations return an availability error.
func NewSessionService(db *gorm.DB, redisStore *AuthRedis, settings *config.Settings) *SessionService {
	return &SessionService{db: db, redis: redisStore, settings: settings, now: persistence.NowMillis}
}

// Create creates a session and its audit event in one MySQL transaction. It
// reserves the Redis issuance limit first and revokes the oldest active session
// when the user reaches the configured active-session cap.
func (s *SessionService) Create(ctx context.Context, user *models.User, input SessionCreateInput) (*IssuedSession, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if user == nil || user.ID <= 0 || user.IsDeleted != 0 || !user.Status.IsActive() {
		return nil, errUnauthorized("认证用户无效")
	}
	if err := s.redis.ReserveSessionIssue(ctx, user.ID, s.settings.SessionIssueLimit24h); err != nil {
		return nil, err
	}
	sid, err := security.NewSessionSID()
	if err != nil {
		return nil, err
	}
	secret, err := security.NewRefreshSecret()
	if err != nil {
		return nil, err
	}
	now := s.now()
	expiresAt := now + int64(s.settings.SessionDays)*int64(24*time.Hour/time.Millisecond)
	var created models.Session
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var storedUser models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND is_deleted = 0", user.ID).First(&storedUser).Error; err != nil {
			return err
		}
		if !storedUser.Status.IsActive() {
			return errUnauthorized("认证用户无效")
		}

		var active []models.Session
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND expires_at > ?", user.ID, now).Order("created_at ASC, id ASC").Find(&active).Error; err != nil {
			return err
		}
		if len(active) >= s.settings.SessionMaxActive {
			oldest := active[0]
			if err := s.redis.MarkSessionRevoked(ctx, oldest.SID, sessionTTL(oldest, now)); err != nil {
				return err
			}
			if err := s.revokeLocked(tx, &oldest, user.ID, models.AuthAuditEventSessionRevoked, input); err != nil {
				return err
			}
		}

		created = models.Session{
			AuditFields:    auditFields(&user.ID),
			SID:            sid,
			UserID:         user.ID,
			LoginMethod:    input.LoginMethod,
			IP:             stringPointer(input.IP),
			UserAgent:      stringPointer(input.UserAgent),
			SessionVersion: 1,
			RefreshHMAC:    security.RefreshHMAC(secret, s.settings.AuthHMACKey),
			LastActiveAt:   now,
			ExpiresAt:      expiresAt,
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.User{}).Where("id = ? AND is_deleted = 0", user.ID).Updates(map[string]any{"last_login_at": now, "updated_at": now, "updated_by": user.ID}).Error; err != nil {
			return err
		}
		return s.writeAuthAudit(tx, &user.ID, &created.Guid, models.AuthAuditEventLoginSucceeded, input.LoginMethod, input)
	})
	if err != nil {
		return nil, err
	}
	return &IssuedSession{Session: &created, RefreshToken: sid + "." + secret}, nil
}

// Refresh rotates a refresh token with compare-and-swap semantics. Concurrent
// reuse within the configured window returns the AEAD-wrapped Redis result;
// reuse afterwards first writes a Redis denial barrier then revokes MySQL.
func (s *SessionService) Refresh(ctx context.Context, refreshToken string) (*IssuedSession, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	sid, secret, err := splitRefreshToken(refreshToken)
	if err != nil {
		return nil, errUnauthorized("刷新凭据无效")
	}
	if revoked, err := s.redis.IsSessionRevoked(ctx, sid); err != nil {
		return nil, err
	} else if revoked {
		return nil, errUnauthorized("刷新凭据无效")
	}
	digest := security.RefreshHMAC(secret, s.settings.AuthHMACKey)
	now := s.now()
	var rotated *IssuedSession
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session models.Session
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("sid = ? AND is_deleted = 0", sid).First(&session).Error; err != nil {
			return errUnauthorized("刷新凭据无效")
		}
		if session.RevokedAt != nil || session.ExpiresAt <= now {
			return errUnauthorized("刷新凭据无效")
		}
		if subtle.ConstantTimeCompare([]byte(session.RefreshHMAC), []byte(digest)) == 1 {
			newSecret, err := security.NewRefreshSecret()
			if err != nil {
				return err
			}
			deadline := now + int64(s.settings.RefreshReplaySeconds)*int64(time.Second/time.Millisecond)
			previous := session.RefreshHMAC
			refreshToken := sid + "." + newSecret
			// Store an AEAD-wrapped pending result before the MySQL CAS. If the
			// post-commit publish fails, a concurrent request holding the old
			// cookie can recover this exact result after it verifies the DB HMAC.
			if err := s.redis.StorePendingRotation(ctx, sid, refreshToken, time.Duration(s.settings.RefreshReplaySeconds)*time.Second); err != nil {
				return err
			}
			session.PreviousRefreshHMAC = &previous
			session.PreviousRefreshExpiresAt = &deadline
			session.RefreshHMAC = security.RefreshHMAC(newSecret, s.settings.AuthHMACKey)
			session.LastActiveAt = now
			TouchAudit(&session.AuditFields, session.UserID)
			if err := tx.Model(&models.Session{}).Where("id = ? AND is_deleted = 0 AND refresh_hmac = ?", session.ID, previous).Updates(map[string]any{
				"refresh_hmac": session.RefreshHMAC, "previous_refresh_hmac": previous, "previous_refresh_expires_at": deadline,
				"last_active_at": now, "updated_at": session.UpdatedAt, "updated_by": session.UpdatedBy,
			}).Error; err != nil {
				return err
			}
			if err := s.writeAuthAudit(tx, &session.UserID, &session.Guid, models.AuthAuditEventRefreshSucceeded, session.LoginMethod, SessionCreateInput{IP: dereferenceString(session.IP), UserAgent: dereferenceString(session.UserAgent)}); err != nil {
				return err
			}
			rotated = &IssuedSession{Session: &session, RefreshToken: refreshToken}
			return nil
		}
		if session.PreviousRefreshHMAC != nil && session.PreviousRefreshExpiresAt != nil && *session.PreviousRefreshExpiresAt >= now && subtle.ConstantTimeCompare([]byte(*session.PreviousRefreshHMAC), []byte(digest)) == 1 {
			shared, found, err := s.redis.RecoverRotationResult(ctx, sid, time.Duration(s.settings.RefreshReplaySeconds)*time.Second)
			if err != nil {
				return err
			}
			if !found {
				return errors.New("concurrent refresh result unavailable")
			}
			rotated = &IssuedSession{Session: &session, RefreshToken: shared}
			return nil
		}

		// Redis is written before the database revocation so replay is denied even
		// if the following transaction cannot commit.
		if err := s.redis.MarkSessionRevoked(ctx, sid, sessionTTL(session, now)); err != nil {
			return err
		}
		return s.revokeLocked(tx, &session, session.UserID, models.AuthAuditEventReplayRevoked, SessionCreateInput{IP: dereferenceString(session.IP), UserAgent: dereferenceString(session.UserAgent)})
	})
	if err != nil {
		return nil, err
	}
	if _, found, err := s.redis.RecoverRotationResult(ctx, sid, time.Duration(s.settings.RefreshReplaySeconds)*time.Second); err != nil || !found {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("rotation result publication unavailable")
	}
	return rotated, nil
}

// Revoke writes a Redis denial barrier before revoking one active session in a
// transaction and recording its security audit event.
func (s *SessionService) Revoke(ctx context.Context, userID, sessionGUID int64) error {
	if err := s.ready(); err != nil {
		return err
	}
	var candidate models.Session
	if err := s.db.WithContext(ctx).Where("guid = ? AND user_id = ? AND is_deleted = 0", sessionGUID, userID).First(&candidate).Error; err != nil {
		return errNotFound("会话不存在")
	}
	if err := s.redis.MarkSessionRevoked(ctx, candidate.SID, sessionTTL(candidate, s.now())); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session models.Session
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND is_deleted = 0", candidate.ID, userID).First(&session).Error; err != nil {
			return err
		}
		return s.revokeLocked(tx, &session, userID, models.AuthAuditEventSessionRevoked, SessionCreateInput{})
	})
}

// RevokeOthers revokes every other active session for the user. It never
// physically deletes rows and writes each Redis barrier before MySQL mutation.
func (s *SessionService) RevokeOthers(ctx context.Context, userID int64, keepSID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize with Create on the same user row so a session selected for
		// revocation cannot escape through a concurrent create/revoke race.
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND is_deleted = 0", userID).First(&user).Error; err != nil {
			return err
		}
		var sessions []models.Session
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND sid <> ? AND is_deleted = 0 AND revoked_at IS NULL", userID, keepSID).Find(&sessions).Error; err != nil {
			return err
		}
		for _, session := range sessions {
			if err := s.redis.MarkSessionRevoked(ctx, session.SID, sessionTTL(session, s.now())); err != nil {
				return err
			}
		}
		for _, candidate := range sessions {
			var session models.Session
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND is_deleted = 0", candidate.ID, userID).First(&session).Error; err != nil {
				return err
			}
			if err := s.revokeLocked(tx, &session, userID, models.AuthAuditEventSessionRevoked, SessionCreateInput{}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Validate confirms a session is not Redis-revoked, expired, soft-deleted, or
// version-mismatched with the authenticated user's current auth version.
func (s *SessionService) Validate(ctx context.Context, sid string, userID int64, sessionVersion, authVersion int) (*models.Session, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if revoked, err := s.redis.IsSessionRevoked(ctx, sid); err != nil {
		return nil, err
	} else if revoked {
		return nil, errUnauthorized("认证会话无效")
	}
	var session models.Session
	if err := s.db.WithContext(ctx).Where("sid = ? AND user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND expires_at > ?", sid, userID, s.now()).First(&session).Error; err != nil {
		return nil, errUnauthorized("认证会话无效")
	}
	if session.SessionVersion != sessionVersion {
		return nil, errUnauthorized("认证会话无效")
	}
	var user models.User
	if err := s.db.WithContext(ctx).Where("id = ? AND is_deleted = 0", userID).First(&user).Error; err != nil || !user.Status.IsActive() || user.AuthVersion != authVersion {
		return nil, errUnauthorized("认证会话无效")
	}
	return &session, nil
}

func (s *SessionService) revokeLocked(tx *gorm.DB, session *models.Session, actorID int64, event models.AuthAuditEventType, input SessionCreateInput) error {
	if session.RevokedAt != nil {
		return nil
	}
	now := s.now()
	session.RevokedAt = &now
	TouchAudit(&session.AuditFields, actorID)
	if err := tx.Model(&models.Session{}).Where("id = ? AND is_deleted = 0 AND revoked_at IS NULL", session.ID).Updates(map[string]any{"revoked_at": now, "updated_at": session.UpdatedAt, "updated_by": session.UpdatedBy}).Error; err != nil {
		return err
	}
	return s.writeAuthAudit(tx, &session.UserID, &session.Guid, event, session.LoginMethod, input)
}

func (s *SessionService) writeAuthAudit(tx *gorm.DB, userID, sessionGUID *int64, event models.AuthAuditEventType, method models.LoginMethod, input SessionCreateInput) error {
	return tx.Create(&models.AuthAuditEvent{AuditFields: auditFields(userID), UserID: userID, SessionGuid: sessionGUID, EventType: event, LoginMethod: &method, IP: stringPointer(input.IP), UserAgent: stringPointer(input.UserAgent)}).Error
}

func (s *SessionService) ready() error {
	if s == nil || s.db == nil || s.settings == nil || s.redis == nil {
		return errors.New("authentication session service is unavailable")
	}
	if s.settings.SessionDays <= 0 || s.settings.SessionMaxActive <= 0 || s.settings.SessionIssueLimit24h <= 0 || s.settings.RefreshReplaySeconds <= 0 || strings.TrimSpace(s.settings.AuthHMACKey) == "" {
		return errors.New("authentication session configuration is invalid")
	}
	return nil
}

func splitRefreshToken(value string) (string, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[0]) != 36 || len(parts[1]) < 64 {
		return "", "", errors.New("invalid refresh token")
	}
	return parts[0], parts[1], nil
}

func stringPointer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sessionTTL(session models.Session, now int64) time.Duration {
	remaining := session.ExpiresAt - now
	if remaining <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(remaining) * time.Millisecond
}
