// Command public-render-job is the narrow, one-shot local interface used by the
// dedicated frontend renderer. It never exposes database credentials or tables.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const publicRenderCLIInputLimit = 16 << 10

type renderJobs interface {
	Lease(context.Context, service.PublicRenderLeaseInput) (*service.PublicRenderLease, error)
	Renew(context.Context, service.PublicRenderTransitionInput) error
	Complete(context.Context, service.PublicRenderTransitionInput) error
	Fail(context.Context, service.PublicRenderTransitionInput) error
	Health(context.Context, int64) (service.PublicRenderHealthStatus, error)
}

type commandInput struct {
	OwnerToken  string `json:"owner_token"`
	JobGUID     int64  `json:"job_guid"`
	Fence       int    `json:"fence"`
	NowMillis   int64  `json:"now_millis"`
	LeaseMillis int64  `json:"lease_millis"`
	Failure     string `json:"failure"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	settings, err := config.LoadMigrationSettings()
	if err != nil {
		writeStartupError("configuration_unavailable")
		os.Exit(1)
	}
	key, err := decodePurposeKey(os.Getenv("PUBLIC_RENDER_JOB_KEY"))
	if err != nil {
		writeStartupError("credential_unavailable")
		os.Exit(1)
	}
	gdb, err := db.Open(settings.DatabaseURL, settings.AppEnv)
	if err != nil {
		writeStartupError("database_unavailable")
		os.Exit(1)
	}
	if err := migration.Verify(ctx, gdb); err != nil {
		writeStartupError("schema_unavailable")
		os.Exit(1)
	}
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, service.NewPublicRenderJobService(gdb, key)))
}

func decodePurposeKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	key, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid purpose key")
	}
	return key, nil
}

func writeStartupError(code string) { _, _ = fmt.Fprintf(os.Stderr, "{\"error\":%q}\n", code) }

func run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer, jobs renderJobs) int {
	if len(args) != 1 || !validCommand(args[0]) {
		writeCLIError(stderr, "invalid_command")
		return 2
	}
	var input commandInput
	raw, readErr := io.ReadAll(io.LimitReader(in, publicRenderCLIInputLimit+1))
	if readErr != nil || len(raw) > publicRenderCLIInputLimit {
		writeCLIError(stderr, "invalid_input")
		return 2
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeCLIError(stderr, "invalid_input")
		return 2
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeCLIError(stderr, "invalid_input")
		return 2
	}
	var result any
	var err error
	switch args[0] {
	case "lease":
		var lease *service.PublicRenderLease
		lease, err = jobs.Lease(ctx, service.PublicRenderLeaseInput{OwnerToken: input.OwnerToken, NowMillis: input.NowMillis, LeaseMillis: input.LeaseMillis})
		result = map[string]any{"job": lease}
	case "renew":
		err = jobs.Renew(ctx, transition(input))
		result = map[string]any{"status": "renewed"}
	case "complete":
		err = jobs.Complete(ctx, transition(input))
		result = map[string]any{"status": "completed"}
	case "fail":
		err = jobs.Fail(ctx, transition(input))
		result = map[string]any{"status": "recorded"}
	case "health":
		var health service.PublicRenderHealthStatus
		health, err = jobs.Health(ctx, input.NowMillis)
		result = health
	}
	if err != nil {
		if errors.Is(err, service.ErrPublicRenderInvalid) {
			writeCLIError(stderr, "invalid_input")
			return 2
		}
		if errors.Is(err, service.ErrPublicRenderLeaseLost) {
			writeCLIError(stderr, "lease_lost")
			return 3
		}
		writeCLIError(stderr, "service_unavailable")
		return 1
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(result); err != nil {
		return 1
	}
	return 0
}

func transition(in commandInput) service.PublicRenderTransitionInput {
	return service.PublicRenderTransitionInput{JobGUID: in.JobGUID, OwnerToken: in.OwnerToken, Fence: in.Fence, NowMillis: in.NowMillis, LeaseMillis: in.LeaseMillis, Failure: in.Failure}
}
func validCommand(command string) bool {
	switch command {
	case "lease", "renew", "complete", "fail", "health":
		return true
	}
	return false
}
func writeCLIError(w io.Writer, code string) { _, _ = fmt.Fprintf(w, "{\"error\":%q}\n", code) }
