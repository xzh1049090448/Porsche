package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(databaseURL string, appEnv string) (*gorm.DB, error) {
	logLevel := logger.Warn
	if appEnv == "development" {
		logLevel = logger.Info
	}

	var dialector gorm.Dialector
	url := strings.TrimSpace(databaseURL)

	switch {
	case strings.HasPrefix(url, "sqlite://"):
		path := strings.TrimPrefix(url, "sqlite://")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		dialector = sqlite.Open(path)
	case strings.HasPrefix(url, "mysql://"):
		dsn := mysqlURLToDSN(strings.TrimPrefix(url, "mysql://"))
		dialector = mysql.Open(dsn)
	default:
		if strings.Contains(url, "aiosqlite") || strings.Contains(url, "sqlite") {
			path := "./data/platform.db"
			if idx := strings.LastIndex(url, "///"); idx >= 0 {
				path = strings.TrimPrefix(url[idx+3:], "./")
				if !strings.HasPrefix(path, "./") && !strings.HasPrefix(path, "/") {
					path = "./" + path
				}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return nil, err
			}
			dialector = sqlite.Open(path)
		} else {
			return nil, fmt.Errorf("unsupported DATABASE_URL: %s", databaseURL)
		}
	}

	gdb, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logLevel)})
	if err != nil {
		return nil, err
	}

	if err := gdb.AutoMigrate(
		&models.User{},
		&models.Conversation{},
		&models.Message{},
		&models.Dataset{},
		&models.DatasetVersion{},
		&models.UsageRecord{},
		&models.Order{},
		&models.AuditLog{},
		&models.ModelHealth{},
	); err != nil {
		return nil, err
	}

	return gdb, nil
}

func mysqlURLToDSN(raw string) string {
	// mysql://user:pass@host:3306/dbname
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	creds := raw[:at]
	hostDB := raw[at+1:]
	colon := strings.Index(creds, ":")
	user := creds
	pass := ""
	if colon >= 0 {
		user = creds[:colon]
		pass = creds[colon+1:]
	}
	return fmt.Sprintf("%s:%s@tcp(%s)?charset=utf8mb4&parseTime=True&loc=Local", user, pass, hostDB)
}
