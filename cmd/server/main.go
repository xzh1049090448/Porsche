package main

import (
	"fmt"
	"log"

	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/router"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	gdb, err := db.Open(settings.DatabaseURL, settings.AppEnv)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	state, err := app.NewState(settings, gdb)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}

	engine := router.New(state)
	addr := fmt.Sprintf("%s:%d", settings.Host, settings.Port)
	log.Printf("ai-gateway-go listening on %s (env=%s)", addr, settings.AppEnv)
	if err := engine.Run(addr); err != nil {
		log.Fatalf("server: %v", err)
	}
}
