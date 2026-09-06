package app

import (
	"context"
	"net/http"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

type State struct {
	Settings              *config.Settings
	DB                    *gorm.DB
	Auth                  *service.AuthService
	Billing               *service.BillingService
	SMS                   *service.SMSService
	Platform              *service.PlatformChatService
	GatewayTokens         *service.GatewayTokenService
	WhiteLabel            *whitelabel.WhiteLabelService
	Audit                 *service.AuditService
	AuthRedis             *service.AuthRedis
	Sessions              *service.SessionService
	ActionSecurityCrypto  *actionsecurity.Crypto
	UserManagementActions *service.UserManagementActions
	UserDeleteActions     *service.UserDeleteActions
	ActionVerifications   *service.ActionVerificationService
	HTTP                  *http.Client
}

func NewState(settings *config.Settings, db *gorm.DB) (*State, error) {
	return newState(settings, db, defaultStateConstructors())
}

type stateConstructors struct {
	newAuthRedisFromURL      func(context.Context, string, string) (*service.AuthRedis, error)
	newUserManagementActions func(*gorm.DB, *service.AuthRedis, *actionsecurity.Crypto) (*service.UserManagementActions, error)
}

func defaultStateConstructors() stateConstructors {
	return stateConstructors{
		newAuthRedisFromURL:      service.NewAuthRedisFromURL,
		newUserManagementActions: service.NewUserManagementActions,
	}
}

func newState(settings *config.Settings, db *gorm.DB, constructors stateConstructors) (*State, error) {
	persistence.ConfigureSnowflake(settings.SnowflakeNodeID)
	s := &State{
		Settings: settings,
		DB:       db,
		SMS:      service.NewSMSService(settings),
		Audit:    service.NewAuditService(),
		HTTP:     &http.Client{},
	}
	authRedisTransferred := false
	defer func() {
		if !authRedisTransferred && s.AuthRedis != nil {
			_ = s.AuthRedis.Close()
		}
	}()
	if len(settings.ActionSecurityHMACKey) > 0 {
		actionCrypto, err := actionsecurity.NewCrypto(settings.ActionSecurityHMACKey)
		if err != nil {
			return nil, err
		}
		s.ActionSecurityCrypto = actionCrypto
	}
	s.Billing = service.NewBillingService(settings)
	// Legacy tests can construct Settings directly; production Settings are
	// fail-closed in config.Load and always supply the white-label settings.
	if settings.WhiteLabel.BaseURL != "" {
		whiteLabel, err := whitelabel.NewWhiteLabelService(settings.WhiteLabel, s.HTTP, nil)
		if err != nil {
			return nil, err
		}
		s.WhiteLabel = whiteLabel
	}
	s.GatewayTokens = service.NewGatewayTokenService(db)
	s.Auth = service.NewAuthService(settings, s.SMS, db)
	// Authentication endpoints are added in Task 4. Initializing the Redis
	// dependency here ensures a configured Redis failure prevents future auth
	// operations from silently falling back to non-revocable JWT behavior.
	if strings.TrimSpace(settings.RedisURL) != "" {
		if constructors.newAuthRedisFromURL == nil {
			return nil, service.ErrActionVerificationUnavailable
		}
		authRedis, err := constructors.newAuthRedisFromURL(context.Background(), settings.RedisURL, settings.AuthHMACKey)
		if err != nil {
			return nil, err
		}
		s.AuthRedis = authRedis
	}
	s.Sessions = service.NewSessionService(db, s.AuthRedis, settings)
	s.Auth.SetSessionService(s.Sessions)
	if s.ActionSecurityCrypto != nil {
		if db == nil || s.AuthRedis == nil || constructors.newUserManagementActions == nil {
			return nil, service.ErrActionVerificationUnavailable
		}
		userManagementActions, err := constructors.newUserManagementActions(db, s.AuthRedis, s.ActionSecurityCrypto)
		if err != nil || userManagementActions == nil || userManagementActions.Verifications == nil ||
			userManagementActions.Operations == nil || userManagementActions.DeleteOutbox == nil || userManagementActions.CreateOutbox == nil ||
			userManagementActions.NewDeleteExecution == nil || userManagementActions.NewCreateExecution == nil {
			return nil, service.ErrActionVerificationUnavailable
		}
		userDeleteActions := userManagementActions.DeleteActions()
		if userDeleteActions == nil {
			return nil, service.ErrActionVerificationUnavailable
		}
		s.UserManagementActions = userManagementActions
		s.UserDeleteActions = userDeleteActions
		s.ActionVerifications = userManagementActions.Verifications
	}
	s.Platform = service.NewPlatformChatService(service.PlatformDeps{
		Settings:   settings,
		DB:         db,
		Billing:    s.Billing,
		WhiteLabel: s.WhiteLabel,
	})

	authRedisTransferred = true
	return s, nil
}
