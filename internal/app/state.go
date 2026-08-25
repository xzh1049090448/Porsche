package app

import (
	"net/http"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

type State struct {
	Settings      *config.Settings
	DB            *gorm.DB
	Auth          *service.AuthService
	Billing       *service.BillingService
	SMS           *service.SMSService
	Platform      *service.PlatformChatService
	GatewayTokens *service.GatewayTokenService
	WhiteLabel    *whitelabel.WhiteLabelService
	Audit         *service.AuditService
	HTTP          *http.Client
}

func NewState(settings *config.Settings, db *gorm.DB) (*State, error) {
	persistence.ConfigureSnowflake(settings.SnowflakeNodeID)
	s := &State{
		Settings: settings,
		DB:       db,
		SMS:      service.NewSMSService(settings),
		Audit:    service.NewAuditService(),
		HTTP:     &http.Client{},
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
	s.Platform = service.NewPlatformChatService(service.PlatformDeps{
		Settings:   settings,
		DB:         db,
		Billing:    s.Billing,
		WhiteLabel: s.WhiteLabel,
	})

	return s, nil
}
