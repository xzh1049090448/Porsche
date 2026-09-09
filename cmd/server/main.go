package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/router"
)

func main() {
	processCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	settings, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	gdb, err := db.Open(settings.DatabaseURL, settings.AppEnv)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	if err := migration.Verify(context.Background(), gdb); err != nil {
		log.Fatalf("verify database schema: %v", err)
	}

	state, err := app.NewState(settings, gdb)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	if state.UpstreamPriceMonitor != nil {
		go state.UpstreamPriceMonitor.Run(processCtx)
	}

	engine := router.New(state)
	addr := fmt.Sprintf("%s:%d", settings.Host, settings.Port)
	log.Printf("ai-gateway-go listening on %s (env=%s)", addr, settings.AppEnv)
	server := &http.Server{Addr: addr, Handler: engine, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-processCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown: %v", err)
		}
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}
