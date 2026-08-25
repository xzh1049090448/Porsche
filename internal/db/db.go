package db

import (
	"context"
	"fmt"
	"net"
	neturl "net/url"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(databaseURL string, appEnv string) (*gorm.DB, error) {
	logLevel := logger.Warn
	if appEnv == "development" {
		logLevel = logger.Info
	}

	url := strings.TrimSpace(databaseURL)
	if !strings.HasPrefix(url, "mysql://") && !strings.HasPrefix(url, "mysql+aiomysql://") && !strings.HasPrefix(url, "mysql+asyncmy://") {
		return nil, fmt.Errorf("unsupported DATABASE_URL")
	}
	dsn, err := mysqlURLToDSN(url)
	if err != nil {
		// URL parse errors can embed the complete input, including credentials.
		// Keep all configuration parsing failures safe for startup logs.
		return nil, fmt.Errorf("invalid MySQL DATABASE_URL")
	}

	gdb, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logLevel)})
	if err != nil {
		return nil, fmt.Errorf("open MySQL database: %s", redactDatabaseError(err))
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("open MySQL database: %s", redactDatabaseError(err))
	}
	if err := sqlDB.PingContext(context.Background()); err != nil {
		return nil, fmt.Errorf("connect MySQL database: %s", redactDatabaseError(err))
	}
	var version string
	if err := gdb.WithContext(context.Background()).Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		return nil, fmt.Errorf("verify MySQL version: %s", redactDatabaseError(err))
	}
	if !isMySQL8(version) {
		return nil, fmt.Errorf("MySQL 8 is required")
	}

	return gdb, nil
}

func redactDatabaseError(error) string { return "database operation failed" }

func isMySQL8(version string) bool { return strings.HasPrefix(strings.TrimSpace(version), "8.") }

func mysqlURLToDSN(raw string) (string, error) {
	// Accept both Go-native mysql:// URLs and Python SQLAlchemy async URLs such
	// as mysql+aiomysql://user:pass@host:3306/database.
	u, err := neturl.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid MySQL DATABASE_URL")
	}
	if u.User == nil || u.User.Username() == "" || u.Hostname() == "" {
		return "", fmt.Errorf("invalid MySQL DATABASE_URL")
	}
	database := strings.TrimPrefix(u.EscapedPath(), "/")
	if database == "" {
		return "", fmt.Errorf("MySQL DATABASE_URL must include a database name")
	}
	database, err = neturl.PathUnescape(database)
	if err != nil {
		return "", fmt.Errorf("invalid MySQL DATABASE_URL")
	}

	addr := u.Hostname()
	if port := u.Port(); port != "" {
		addr = net.JoinHostPort(addr, port)
	} else {
		addr = net.JoinHostPort(addr, "3306")
	}
	password, _ := u.User.Password()
	params := map[string]string{"charset": "utf8mb4"}
	for key, values := range u.Query() {
		if len(values) > 0 {
			params[key] = values[len(values)-1]
		}
	}
	cfg := drivermysql.Config{
		User:      u.User.Username(),
		Passwd:    password,
		Net:       "tcp",
		Addr:      addr,
		DBName:    database,
		Params:    params,
		ParseTime: true,
		Loc:       time.UTC,
	}
	return cfg.FormatDSN(), nil
}
