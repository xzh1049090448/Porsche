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
	monitorCtx, cancelMonitor := context.WithCancel(processCtx)
	var monitor monitorRunner
	if state.UpstreamPriceMonitor != nil {
		monitor = state.UpstreamPriceMonitor
	}
	monitorDone := startMonitor(monitorCtx, monitor)

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
	serveErr := server.ListenAndServe()
	cancelMonitor()
	monitorShutdownCtx, cancelMonitorShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelMonitorShutdown()
	if err := joinMonitor(monitorShutdownCtx, monitorDone); err != nil {
		log.Printf("upstream price monitor shutdown: %v", err)
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatalf("server: %v", serveErr)
	}
}

type monitorRunner interface {
	Run(context.Context)
}

func startMonitor(ctx context.Context, monitor monitorRunner) <-chan struct{} {
	done := make(chan struct{})
	if monitor == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		monitor.Run(ctx)
	}()
	return done
}

func joinMonitor(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
