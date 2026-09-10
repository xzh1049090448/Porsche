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
	"unicode/utf8"

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
	Lookup(context.Context, int64) (*service.PublicRenderGeneration, error)
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
	raw, readErr := io.ReadAll(io.LimitReader(in, publicRenderCLIInputLimit+1))
	if readErr != nil || len(raw) > publicRenderCLIInputLimit || !utf8.Valid(raw) || invalidJSONShape(raw) {
		writeCLIError(stderr, "invalid_input")
		return 2
	}
	var result any
	var err error
	switch args[0] {
	case "lease":
		var input struct {
			OwnerToken  string `json:"owner_token"`
			NowMillis   int64  `json:"now_millis"`
			LeaseMillis int64  `json:"lease_millis"`
		}
		if !decodeExact(raw, &input) {
			return invalidInput(stderr)
		}
		if !validLeaseFields(input.OwnerToken, input.NowMillis, input.LeaseMillis) {
			return invalidInput(stderr)
		}
		var lease *service.PublicRenderLease
		lease, err = jobs.Lease(ctx, service.PublicRenderLeaseInput{OwnerToken: input.OwnerToken, NowMillis: input.NowMillis, LeaseMillis: input.LeaseMillis})
		result = map[string]any{"job": lease}
	case "renew":
		var input struct {
			OwnerToken  string `json:"owner_token"`
			JobGUID     int64  `json:"job_guid"`
			Fence       int    `json:"fence"`
			NowMillis   int64  `json:"now_millis"`
			LeaseMillis int64  `json:"lease_millis"`
		}
		if !decodeExact(raw, &input) {
			return invalidInput(stderr)
		}
		if !validLeaseFields(input.OwnerToken, input.NowMillis, input.LeaseMillis) || input.JobGUID <= 0 || input.Fence <= 0 {
			return invalidInput(stderr)
		}
		err = jobs.Renew(ctx, service.PublicRenderTransitionInput{OwnerToken: input.OwnerToken, JobGUID: input.JobGUID, Fence: input.Fence, NowMillis: input.NowMillis, LeaseMillis: input.LeaseMillis})
		result = map[string]any{"status": "renewed"}
	case "complete":
		var input struct {
			OwnerToken string `json:"owner_token"`
			JobGUID    int64  `json:"job_guid"`
			Fence      int    `json:"fence"`
			NowMillis  int64  `json:"now_millis"`
		}
		if !decodeExact(raw, &input) {
			return invalidInput(stderr)
		}
		if !service.ValidPublicRenderOwnerToken(input.OwnerToken) || input.JobGUID <= 0 || input.Fence <= 0 || input.NowMillis <= 0 {
			return invalidInput(stderr)
		}
		err = jobs.Complete(ctx, service.PublicRenderTransitionInput{OwnerToken: input.OwnerToken, JobGUID: input.JobGUID, Fence: input.Fence, NowMillis: input.NowMillis})
		result = map[string]any{"status": "completed"}
	case "fail":
		var input struct {
			OwnerToken string `json:"owner_token"`
			JobGUID    int64  `json:"job_guid"`
			Fence      int    `json:"fence"`
			NowMillis  int64  `json:"now_millis"`
			Failure    string `json:"failure"`
		}
		if !decodeExact(raw, &input) {
			return invalidInput(stderr)
		}
		if !service.ValidPublicRenderOwnerToken(input.OwnerToken) || input.JobGUID <= 0 || input.Fence <= 0 || input.NowMillis <= 0 || input.Failure == "" {
			return invalidInput(stderr)
		}
		err = jobs.Fail(ctx, service.PublicRenderTransitionInput{OwnerToken: input.OwnerToken, JobGUID: input.JobGUID, Fence: input.Fence, NowMillis: input.NowMillis, Failure: input.Failure})
		result = map[string]any{"status": "recorded"}
	case "health":
		var input struct {
			NowMillis int64 `json:"now_millis"`
		}
		if !decodeExact(raw, &input) {
			return invalidInput(stderr)
		}
		if input.NowMillis <= 0 {
			return invalidInput(stderr)
		}
		var health service.PublicRenderHealthStatus
		health, err = jobs.Health(ctx, input.NowMillis)
		result = health
	case "lookup":
		var input struct {
			NowMillis int64 `json:"now_millis"`
		}
		if !decodeExact(raw, &input) {
			return invalidInput(stderr)
		}
		if input.NowMillis <= 0 {
			return invalidInput(stderr)
		}
		result, err = jobs.Lookup(ctx, input.NowMillis)
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
	case "lease", "renew", "complete", "fail", "health", "lookup":
		return true
	}
	return false
}
func writeCLIError(w io.Writer, code string) { _, _ = fmt.Fprintf(w, "{\"error\":%q}\n", code) }
func invalidInput(w io.Writer) int           { writeCLIError(w, "invalid_input"); return 2 }
func validLeaseFields(owner string, now, lease int64) bool {
	return service.ValidPublicRenderOwnerToken(owner) && now > 0 && lease >= 5_000 && lease <= 300_000
}
func decodeExact(raw []byte, target any) bool {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return false
	}
	var trailing any
	return d.Decode(&trailing) == io.EOF
}
func invalidJSONShape(raw []byte) bool {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	depth := 0
	roots := 0
	stack := []map[string]struct{}{}
	expectKey := []bool{}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return true
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				depth++
				if depth > 8 {
					return true
				}
				stack = append(stack, map[string]struct{}{})
				expectKey = append(expectKey, true)
			case '[':
				depth++
				if depth > 8 {
					return true
				}
				stack = append(stack, nil)
				expectKey = append(expectKey, false)
			case '}', ']':
				depth--
				stack = stack[:len(stack)-1]
				expectKey = expectKey[:len(expectKey)-1]
			}
		case string:
			if len(stack) > 0 && stack[len(stack)-1] != nil && expectKey[len(expectKey)-1] {
				if _, ok := stack[len(stack)-1][v]; ok {
					return true
				}
				stack[len(stack)-1][v] = struct{}{}
				expectKey[len(expectKey)-1] = false
			} else if len(expectKey) > 0 && stack[len(stack)-1] != nil {
				expectKey[len(expectKey)-1] = true
			}
		default:
			if len(expectKey) > 0 && stack[len(stack)-1] != nil {
				expectKey[len(expectKey)-1] = true
			}
		}
		if delim, ok := tok.(json.Delim); ok && (delim == '{' || delim == '[') && depth == 1 {
			roots++
		}
		if delim, ok := tok.(json.Delim); ok && (delim == '}' || delim == ']') && len(expectKey) > 0 && stack[len(stack)-1] != nil {
			expectKey[len(expectKey)-1] = true
		}
	}
	return depth != 0 || roots != 1
}
