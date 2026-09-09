package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

const stateCloseTimeout = 5 * time.Second

var (
	errClosePlatformGenerationWorker = errors.New("close platform generation worker")
	errClosePlatformGenerationStore  = errors.New("close platform generation store")
	errCloseAuthRedis                = errors.New("close authentication Redis")
)

type State struct {
	Settings                        *config.Settings
	DB                              *gorm.DB
	Auth                            *service.AuthService
	Billing                         *service.BillingService
	SMS                             *service.SMSService
	Platform                        *service.PlatformChatService
	GatewayTokens                   *service.GatewayTokenService
	WhiteLabel                      *whitelabel.WhiteLabelService
	Audit                           *service.AuditService
	AuthRedis                       *service.AuthRedis
	PlatformGenerations             *service.PlatformGenerationStore
	PlatformGenerationPersistence   *service.PlatformGenerationPersistence
	PlatformGenerationControl       service.PlatformGenerationController
	PlatformGenerationCancellations *service.PlatformGenerationCancellationRegistry
	PlatformGenerationConverger     *service.PlatformGenerationConverger
	Sessions                        *service.SessionService
	ActionSecurityCrypto            *actionsecurity.Crypto
	UserManagementActions           *service.UserManagementActions
	UserDeleteActions               *service.UserDeleteActions
	ActionVerifications             *service.ActionVerificationService
	HTTP                            *http.Client

	closeOnce sync.Once
	closeErr  error

	closeTimeout                     time.Duration
	closePlatformGenerationConverger func(context.Context, *service.PlatformGenerationConverger) error
	closePlatformGenerations         func(*service.PlatformGenerationStore) error
	closeAuthRedis                   func(*service.AuthRedis) error
}

func NewState(settings *config.Settings, db *gorm.DB) (*State, error) {
	return newState(settings, db, defaultStateConstructors())
}

type stateConstructors struct {
	newAuthRedisFromURL               func(context.Context, string, string) (*service.AuthRedis, error)
	newPlatformGenerationStoreFromURL func(context.Context, string) (*service.PlatformGenerationStore, error)
	newPlatformGenerationControl      func(*gorm.DB, *service.PlatformGenerationStore, *service.PlatformGenerationCancellationRegistry) (*service.PlatformGenerationControl, error)
	newPlatformGenerationConverger    func(*service.PlatformGenerationControl) (*service.PlatformGenerationConverger, error)
	startPlatformGenerationConverger  func(*service.PlatformGenerationConverger)
	closePlatformGenerationConverger  func(context.Context, *service.PlatformGenerationConverger) error
	newUserManagementActions          func(*gorm.DB, *service.AuthRedis, *actionsecurity.Crypto) (*service.UserManagementActions, error)
}

func defaultStateConstructors() stateConstructors {
	return stateConstructors{
		newAuthRedisFromURL:               service.NewAuthRedisFromURL,
		newPlatformGenerationStoreFromURL: service.NewPlatformGenerationStoreFromURL,
		newPlatformGenerationControl:      service.NewPlatformGenerationControl,
		newPlatformGenerationConverger:    service.NewPlatformGenerationConverger,
		startPlatformGenerationConverger:  func(converger *service.PlatformGenerationConverger) { converger.Start() },
		closePlatformGenerationConverger: func(ctx context.Context, converger *service.PlatformGenerationConverger) error {
			return converger.Close(ctx)
		},
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

		closeTimeout:                     stateCloseTimeout,
		closePlatformGenerationConverger: constructors.closePlatformGenerationConverger,
		closePlatformGenerations: func(generations *service.PlatformGenerationStore) error {
			return generations.Close()
		},
		closeAuthRedis: func(authRedis *service.AuthRedis) error {
			return authRedis.Close()
		},
	}
	var generationCancellations *service.PlatformGenerationCancellationRegistry
	var generationControl *service.PlatformGenerationControl
	var generationConverger *service.PlatformGenerationConverger
	dependenciesTransferred := false
	defer func() {
		if dependenciesTransferred {
			return
		}
		if generationConverger != nil && constructors.closePlatformGenerationConverger != nil {
			ctx, cancel := context.WithTimeout(context.Background(), stateCloseTimeout)
			_ = constructors.closePlatformGenerationConverger(ctx, generationConverger)
			cancel()
		}
		if s.PlatformGenerations != nil {
			_ = s.PlatformGenerations.Close()
		}
		if s.AuthRedis != nil {
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
		if constructors.newAuthRedisFromURL == nil || constructors.newPlatformGenerationStoreFromURL == nil {
			return nil, service.ErrActionVerificationUnavailable
		}
		authRedis, err := constructors.newAuthRedisFromURL(context.Background(), settings.RedisURL, settings.AuthHMACKey)
		if err != nil {
			return nil, err
		}
		s.AuthRedis = authRedis
		generations, err := constructors.newPlatformGenerationStoreFromURL(context.Background(), settings.RedisURL)
		if err != nil {
			return nil, err
		}
		s.PlatformGenerations = generations
		generationPersistence, err := service.NewPlatformGenerationPersistence(generations)
		if err != nil {
			return nil, err
		}
		s.PlatformGenerationPersistence = generationPersistence
		if db != nil {
			if constructors.newPlatformGenerationControl == nil || constructors.newPlatformGenerationConverger == nil || constructors.startPlatformGenerationConverger == nil || constructors.closePlatformGenerationConverger == nil {
				return nil, service.ErrPlatformGenerationControlUnavailable
			}
			generationCancellations = service.NewPlatformGenerationCancellationRegistry()
			generationControl, err = constructors.newPlatformGenerationControl(db, generations, generationCancellations)
			if err != nil || generationControl == nil {
				return nil, service.ErrPlatformGenerationControlUnavailable
			}
			generationConverger, err = constructors.newPlatformGenerationConverger(generationControl)
			if err != nil || generationConverger == nil {
				return nil, service.ErrPlatformGenerationControlUnavailable
			}
		}
	}
	s.Sessions = service.NewSessionService(db, s.AuthRedis, settings)
	s.Auth.SetSessionService(s.Sessions)
	if s.ActionSecurityCrypto != nil {
		if db == nil || s.AuthRedis == nil || constructors.newUserManagementActions == nil {
			return nil, service.ErrActionVerificationUnavailable
		}
		userManagementActions, err := constructors.newUserManagementActions(db, s.AuthRedis, s.ActionSecurityCrypto)
		if err != nil || userManagementActions == nil || userManagementActions.Verifications == nil ||
			userManagementActions.Operations == nil || userManagementActions.DeleteOutbox == nil || userManagementActions.CreateOutbox == nil || userManagementActions.ResetOutbox == nil || userManagementActions.RolePermissionOutbox == nil ||
			userManagementActions.NewDeleteExecution == nil || userManagementActions.NewCreateExecution == nil || userManagementActions.NewResetExecution == nil ||
			userManagementActions.NewPromoteExecution == nil || userManagementActions.NewDemoteExecution == nil || userManagementActions.NewPermissionsWriteExecution == nil {
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
	if generationControl != nil {
		s.PlatformGenerationCancellations = generationCancellations
		s.PlatformGenerationControl = generationControl
		s.PlatformGenerationConverger = generationConverger
		constructors.startPlatformGenerationConverger(generationConverger)
	}

	dependenciesTransferred = true
	return s, nil
}

// Close releases only resources owned by State. The database is supplied by
// startup and deliberately remains externally owned.
func (s *State) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		timeout := s.closeTimeout
		if timeout <= 0 {
			timeout = stateCloseTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if s.PlatformGenerationConverger != nil {
			closeConverger := s.closePlatformGenerationConverger
			if closeConverger == nil {
				closeConverger = func(ctx context.Context, converger *service.PlatformGenerationConverger) error {
					return converger.Close(ctx)
				}
			}
			if err := closeConverger(ctx, s.PlatformGenerationConverger); err != nil {
				s.closeErr = errors.Join(s.closeErr, errClosePlatformGenerationWorker)
			}
		}
		if s.PlatformGenerations != nil {
			closeGenerations := s.closePlatformGenerations
			if closeGenerations == nil {
				closeGenerations = func(generations *service.PlatformGenerationStore) error { return generations.Close() }
			}
			if err := closeGenerations(s.PlatformGenerations); err != nil {
				s.closeErr = errors.Join(s.closeErr, errClosePlatformGenerationStore)
			}
		}
		if s.AuthRedis != nil {
			closeAuthRedis := s.closeAuthRedis
			if closeAuthRedis == nil {
				closeAuthRedis = func(authRedis *service.AuthRedis) error { return authRedis.Close() }
			}
			if err := closeAuthRedis(s.AuthRedis); err != nil {
				s.closeErr = errors.Join(s.closeErr, errCloseAuthRedis)
			}
		}
	})
	return s.closeErr
}
