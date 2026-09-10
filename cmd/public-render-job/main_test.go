package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/service"
)

type fakeRenderJobs struct{ command string }

func (f *fakeRenderJobs) Lease(context.Context, service.PublicRenderLeaseInput) (*service.PublicRenderLease, error) {
	f.command = "lease"
	return nil, nil
}
func (f *fakeRenderJobs) Renew(context.Context, service.PublicRenderTransitionInput) error {
	f.command = "renew"
	return nil
}
func (f *fakeRenderJobs) Complete(context.Context, service.PublicRenderTransitionInput) error {
	f.command = "complete"
	return nil
}
func (f *fakeRenderJobs) Fail(context.Context, service.PublicRenderTransitionInput) error {
	f.command = "fail"
	return nil
}
func (f *fakeRenderJobs) Health(context.Context, int64) (service.PublicRenderHealthStatus, error) {
	f.command = "health"
	return service.PublicRenderHealthStatus{Status: "healthy"}, nil
}

func TestPublicRenderCLICommandsAreStrictAndJSONOnly(t *testing.T) {
	for _, command := range []string{"lease", "renew", "complete", "fail", "health"} {
		t.Run(command, func(t *testing.T) {
			jobs := &fakeRenderJobs{}
			var out, stderr bytes.Buffer
			body := `{"owner_token":"owner-one-long-random-token","job_guid":123,"fence":1,"now_millis":1900000000000,"lease_millis":30000,"failure":"render_failed"}`
			if command == "health" {
				body = `{"now_millis":1900000000000}`
			}
			code := run(context.Background(), []string{command}, strings.NewReader(body), &out, &stderr, jobs)
			if code != 0 || jobs.command != command || stderr.Len() != 0 {
				t.Fatalf("run = %d command=%q stderr=%q", code, jobs.command, stderr.String())
			}
			var decoded map[string]any
			if json.Unmarshal(out.Bytes(), &decoded) != nil {
				t.Fatalf("non-JSON output: %q", out.String())
			}
		})
	}
}

func TestPublicRenderCLIRejectsUnknownCommandsFieldsAndOversizedInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		body string
	}{
		{"unknown command", []string{"watch"}, `{}`},
		{"extra arg", []string{"health", "again"}, `{}`},
		{"unknown field", []string{"health"}, `{"now_millis":1,"path":"/private"}`},
		{"trailing document", []string{"health"}, `{"now_millis":1}{}`},
		{"oversized", []string{"health"}, strings.Repeat("x", publicRenderCLIInputLimit+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := run(context.Background(), tc.args, strings.NewReader(tc.body), &out, &stderr, &fakeRenderJobs{})
			if code != 2 || out.Len() != 0 {
				t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
			}
			if strings.Contains(stderr.String(), "/private") {
				t.Fatalf("stderr leaked input: %q", stderr.String())
			}
		})
	}
}

func TestPublicRenderCLIMapsLeaseLossToConflictExit(t *testing.T) {
	jobs := &errorRenderJobs{err: service.ErrPublicRenderLeaseLost}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"complete"}, strings.NewReader(`{"owner_token":"owner-one-long-random-token","job_guid":123,"fence":1,"now_millis":1900000000000}`), &out, &stderr, jobs)
	if code != 3 || !strings.Contains(stderr.String(), "lease_lost") || errors.Is(jobs.err, nil) {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

type errorRenderJobs struct{ err error }

func (e *errorRenderJobs) Lease(context.Context, service.PublicRenderLeaseInput) (*service.PublicRenderLease, error) {
	return nil, e.err
}
func (e *errorRenderJobs) Renew(context.Context, service.PublicRenderTransitionInput) error {
	return e.err
}
func (e *errorRenderJobs) Complete(context.Context, service.PublicRenderTransitionInput) error {
	return e.err
}
func (e *errorRenderJobs) Fail(context.Context, service.PublicRenderTransitionInput) error {
	return e.err
}
func (e *errorRenderJobs) Health(context.Context, int64) (service.PublicRenderHealthStatus, error) {
	return service.PublicRenderHealthStatus{}, e.err
}
