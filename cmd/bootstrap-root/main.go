package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const invalidCredentialsFile = "invalid Root credentials file"

type credentials struct {
	Username string
	Password string
}

func main() {
	if err := run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	credentialsPath, err := parseArgs(args)
	if err != nil {
		return err
	}

	settings, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	file, err := os.Open(credentialsPath)
	if err != nil {
		return errors.New("open Root credentials file")
	}
	parsed, parseErr := parseCredentials(file)
	closeErr := file.Close()
	if parseErr != nil {
		return parseErr
	}
	if closeErr != nil {
		return errors.New("close Root credentials file")
	}
	if err := config.ValidateRootBootstrapCredentials(parsed.Username, parsed.Password); err != nil {
		return errors.New("invalid Root credentials file")
	}

	gdb, err := db.Open(settings.DatabaseURL, settings.AppEnv)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return errors.New("database handle unavailable")
	}
	defer sqlDB.Close()
	bootstrapDB := quietBootstrapDB(gdb)

	settings.RootBootstrapUsername = parsed.Username
	settings.RootBootstrapPassword = parsed.Password
	defer func() {
		settings.RootBootstrapUsername = ""
		settings.RootBootstrapPassword = ""
	}()
	created, err := service.NewAuthService(settings, nil, bootstrapDB).BootstrapRoot(context.Background())
	if err != nil {
		return errors.New("Root bootstrap failed")
	}

	var count int64
	if err := bootstrapDB.Model(&models.User{}).Where("role = ?", models.UserRoleRoot).Count(&count).Error; err != nil {
		return errors.New("count Root users")
	}
	if count != 1 {
		return errors.New("Root bootstrap did not leave exactly one Root user")
	}

	if created != nil {
		fmt.Println("Root bootstrap created")
		return nil
	}
	fmt.Println("Root bootstrap already consumed")
	return nil
}

func quietBootstrapDB(gdb *gorm.DB) *gorm.DB {
	return gdb.Session(&gorm.Session{Logger: logger.Discard})
}

func parseArgs(args []string) (string, error) {
	if len(args) != 3 || strings.TrimSpace(args[0]) == "" || args[1] != "--credentials-file" || strings.TrimSpace(args[2]) == "" {
		return "", errors.New("usage: bootstrap-root --credentials-file PATH")
	}
	return args[2], nil
}

func parseCredentials(reader io.Reader) (credentials, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil || len(contents) > 4096 {
		return credentials{}, errors.New(invalidCredentialsFile)
	}

	scanner := bufio.NewScanner(bytes.NewReader(contents))
	scanner.Buffer(make([]byte, 0, 4096), 4096)
	var parsed credentials
	seenUsername, seenPassword := false, false
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if !found || key == "" || value == "" || value != strings.TrimSpace(value) {
			return credentials{}, errors.New(invalidCredentialsFile)
		}
		switch key {
		case "username":
			if seenUsername {
				return credentials{}, errors.New(invalidCredentialsFile)
			}
			parsed.Username = value
			seenUsername = true
		case "password":
			if seenPassword {
				return credentials{}, errors.New(invalidCredentialsFile)
			}
			parsed.Password = value
			seenPassword = true
		default:
			return credentials{}, errors.New(invalidCredentialsFile)
		}
	}
	if scanner.Err() != nil || !seenUsername || !seenPassword {
		return credentials{}, errors.New(invalidCredentialsFile)
	}
	return parsed, nil
}
