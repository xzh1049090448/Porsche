package db

import (
	"strings"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
)

func TestOpenRejectsSQLiteURLWithoutCreatingFiles(t *testing.T) {
	_, err := Open("sqlite://./data/platform.db", "test")
	if err == nil || !strings.Contains(err.Error(), "unsupported DATABASE_URL") {
		t.Fatalf("Open() error = %v, want unsupported DATABASE_URL", err)
	}
}

func TestMySQLURLToDSNAcceptsPythonAIOMySQLURL(t *testing.T) {
	dsn, err := mysqlURLToDSN("mysql+aiomysql://platform:platform@mysql:3306/platform")
	if err != nil {
		t.Fatalf("mysqlURLToDSN() error = %v", err)
	}
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN() error = %v, dsn = %q", err, dsn)
	}
	if cfg.User != "platform" || cfg.Passwd != "platform" || cfg.Addr != "mysql:3306" || cfg.DBName != "platform" {
		t.Fatalf("unexpected parsed DSN: user=%q addr=%q db=%q", cfg.User, cfg.Addr, cfg.DBName)
	}
	if !cfg.ParseTime {
		t.Fatal("ParseTime must be enabled")
	}
}
