package app

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/gateway"
	"github.com/porsche/ai-gateway-go/internal/rag"
	"github.com/porsche/ai-gateway-go/internal/registry"
	"github.com/porsche/ai-gateway-go/internal/service"
	"gorm.io/gorm"
)

type State struct {
	Settings *config.Settings
	DB       *gorm.DB
	Models   *registry.ModelRegistry
	Clients  *registry.ClientRegistry
	Pool     *gateway.KeyPool
	Gateway  *gateway.Service
	RAG      *rag.Engine
	Auth     *service.AuthService
	Billing  *service.BillingService
	SMS      *service.SMSService
	Platform *service.PlatformChatService
	Audit    *service.AuditService
	HTTP     *http.Client
}

func NewState(settings *config.Settings, db *gorm.DB) (*State, error) {
	_ = os.MkdirAll(settings.ChromaPersistDir, 0o755)
	_ = os.MkdirAll(settings.DatasetUploadDir, 0o755)
	_ = os.MkdirAll(filepath.Dir("./data/platform.db"), 0o755)

	models := registry.NewModelRegistry(settings.ModelsConfigPath, settings.EnvKeys)
	clients := registry.NewClientRegistry(settings.ClientsConfigPath)
	pool := gateway.NewKeyPool(settings)
	pool.Rebuild(models)

	s := &State{
		Settings: settings,
		DB:       db,
		Models:   models,
		Clients:  clients,
		Pool:     pool,
		Gateway:  gateway.NewService(settings, models, pool),
		RAG:      rag.NewEngine(settings.ChromaPersistDir, settings.RAGTopK),
		SMS:      service.NewSMSService(settings),
		Audit:    service.NewAuditService(),
		HTTP:     &http.Client{},
	}
	s.Billing = service.NewBillingService(settings)
	s.Auth = service.NewAuthService(settings, s.SMS, db)
	s.Platform = service.NewPlatformChatService(service.PlatformDeps{
		Settings: settings,
		DB:       db,
		Models:   models,
		Clients:  clients,
		Gateway:  s.Gateway,
		RAG:      s.RAG,
		Billing:  s.Billing,
	})

	if err := service.SeedDefaultDatasets(db, s.RAG); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *State) ReloadConfig() {
	s.Models.Reload(s.Settings.EnvKeys)
	s.Clients.Reload()
	s.Pool.Rebuild(s.Models)
}
