// Command migrate applies or inspects Porsche's embedded MySQL schema.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/persistence"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "status") {
		log.Fatal("usage: migrate <up|status>")
	}
	settings, err := config.LoadMigrationSettings()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	gdb, err := db.Open(settings.DatabaseURL, settings.AppEnv)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	ctx := context.Background()
	if os.Args[1] == "up" {
		generator := persistence.NewSnowflake(settings.SnowflakeNodeID, persistence.SystemClock())
		if err := migration.Up(ctx, gdb, generator.Next, func() int64 { return time.Now().UTC().UnixMilli() }); err != nil {
			log.Fatalf("apply migrations: %v", err)
		}
	}
	status, err := migration.Status(ctx, gdb)
	if err != nil {
		log.Fatalf("migration status: %v", err)
	}
	for _, entry := range status {
		fmt.Printf("%s %s\n", entry.Version, entry.Checksum)
	}
}
