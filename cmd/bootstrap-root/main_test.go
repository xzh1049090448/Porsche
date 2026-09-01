package main

import (
	"strings"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestQuietBootstrapDBUsesDiscardLogger(t *testing.T) {
	gdb, err := gorm.Open(nil, &gorm.Config{Logger: logger.Default.LogMode(logger.Info)})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}

	quiet := quietBootstrapDB(gdb)
	if quiet == gdb {
		t.Fatal("quietBootstrapDB() returned the source database instead of an isolated session")
	}
	if quiet.Config.Logger != logger.Discard {
		t.Fatalf("quietBootstrapDB() logger = %T, want logger.Discard", quiet.Config.Logger)
	}
	if gdb.Config.Logger == logger.Discard {
		t.Fatal("quietBootstrapDB() changed the source database logger")
	}
}

func TestParseCredentials(t *testing.T) {
	const validPassword = "Aa1@0123456789ab"

	tests := []struct {
		name    string
		input   string
		want    credentials
		wantErr bool
	}{
		{
			name:  "accepts exactly one username and password",
			input: "username=root_admin\npassword=" + validPassword + "\n",
			want:  credentials{Username: "root_admin", Password: validPassword},
		},
		{name: "rejects duplicate username", input: "username=root_admin\nusername=other_root\npassword=" + validPassword + "\n", wantErr: true},
		{name: "rejects unknown key", input: "username=root_admin\npassword=" + validPassword + "\nextra=value\n", wantErr: true},
		{name: "rejects missing password", input: "username=root_admin\n", wantErr: true},
		{name: "rejects malformed line", input: "username=root_admin\npassword" + validPassword + "\n", wantErr: true},
		{name: "rejects empty value", input: "username=root_admin\npassword=\n", wantErr: true},
		{name: "rejects username surrounding whitespace", input: "username= root_admin\npassword=" + validPassword + "\n", wantErr: true},
		{name: "rejects password surrounding whitespace", input: "username=root_admin\npassword=" + validPassword + " \n", wantErr: true},
		{name: "rejects files over 4096 bytes", input: "username=root_admin\npassword=" + validPassword + "\n#" + strings.Repeat("x", 4096), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCredentials(strings.NewReader(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseCredentials() error = nil, want invalid credentials error")
				}
				if err.Error() != "invalid Root credentials file" {
					t.Fatalf("parseCredentials() error = %q, want generic invalid credentials error", err)
				}
				if strings.Contains(err.Error(), "Aa1@") {
					t.Fatalf("parseCredentials() error leaked credential input: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCredentials() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("parseCredentials() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "accepts credentials file", args: []string{"bootstrap-root", "--credentials-file", "/run/secrets/root"}, want: "/run/secrets/root"},
		{name: "rejects missing program name", args: []string{"", "--credentials-file", "/run/secrets/root"}, wantErr: true},
		{name: "rejects missing flag", args: []string{"bootstrap-root"}, wantErr: true},
		{name: "rejects unknown flag", args: []string{"bootstrap-root", "--file", "/run/secrets/root"}, wantErr: true},
		{name: "rejects empty path", args: []string{"bootstrap-root", "--credentials-file", ""}, wantErr: true},
		{name: "rejects trailing arguments", args: []string{"bootstrap-root", "--credentials-file", "/run/secrets/root", "extra"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseArgs() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("parseArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}
