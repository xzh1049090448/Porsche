package db

import (
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
		return nil, fmt.Errorf("unsupported DATABASE_URL: %s", databaseURL)
	}
	dsn, err := mysqlURLToDSN(url)
	if err != nil {
		return nil, err
	}

	gdb, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logLevel)})
	if err != nil {
		return nil, err
	}

	return gdb, nil
}

func mysqlURLToDSN(raw string) (string, error) {
	// Accept both Go-native mysql:// URLs and Python SQLAlchemy async URLs such
	// as mysql+aiomysql://user:pass@host:3306/database.
	u, err := neturl.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse MySQL DATABASE_URL: %w", err)
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
		return "", fmt.Errorf("decode MySQL database name: %w", err)
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
		Loc:       time.Local,
	}
	return cfg.FormatDSN(), nil
}
