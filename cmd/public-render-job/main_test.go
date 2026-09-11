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
func (f *fakeRenderJobs) Health(context.Context) (service.PublicRenderHealthStatus, error) {
	f.command = "health"
	return service.PublicRenderHealthStatus{Status: "healthy"}, nil
}
func (f *fakeRenderJobs) Lookup(context.Context) (*service.PublicRenderGeneration, error) {
	f.command = "lookup"
	return &service.PublicRenderGeneration{Generation: 1}, nil
}

func TestPublicRenderCLICommandsAreStrictAndJSONOnly(t *testing.T) {
	valid := map[string]string{
		"lease":    `{"owner_token":"owner-one-long-random-token","lease_millis":30000}`,
		"renew":    `{"owner_token":"owner-one-long-random-token","job_guid":123,"fence":1,"lease_millis":30000}`,
		"complete": `{"owner_token":"owner-one-long-random-token","job_guid":123,"fence":1}`,
		"fail":     `{"owner_token":"owner-one-long-random-token","job_guid":123,"fence":1,"failure":"render_failed"}`,
		"health":   `{}`,
		"lookup":   `{}`,
	}
	for command, body := range valid {
		t.Run(command, func(t *testing.T) {
			jobs := &fakeRenderJobs{}
			var out, stderr bytes.Buffer
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
		{"cross command health", []string{"health"}, `{"now_millis":1,"owner_token":"1234567890123456"}`},
		{"caller time lease", []string{"lease"}, `{"owner_token":"1234567890123456","lease_millis":5000,"now_millis":1}`},
		{"caller time renew", []string{"renew"}, `{"owner_token":"1234567890123456","job_guid":1,"fence":1,"lease_millis":5000,"now_millis":1}`},
		{"caller time complete", []string{"complete"}, `{"owner_token":"1234567890123456","job_guid":1,"fence":1,"now_millis":9223372036854775807}`},
		{"caller time fail", []string{"fail"}, `{"owner_token":"1234567890123456","job_guid":1,"fence":1,"failure":"render_failed","now_millis":9223372036854775807}`},
		{"caller time lookup", []string{"lookup"}, `{"now_millis":9223372036854775807}`},
		{"cross command complete", []string{"complete"}, `{"owner_token":"1234567890123456","job_guid":1,"fence":1,"now_millis":1,"failure":"x"}`},
		{"duplicate field", []string{"health"}, `{"now_millis":1,"now_millis":2}`},
		{"invalid utf8", []string{"health"}, string([]byte{'{', '"', 'n', 'o', 'w', '_', 'm', 'i', 'l', 'l', 'i', 's', '"', ':', '1', ',', '"', 'x', '"', ':', '"', 0xff, '"', '}'})},
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

func TestPublicRenderJSONShapeRejectsNestedDuplicatesAndDepth(t *testing.T) {
	if !invalidJSONShape([]byte(`{"outer":{"x":1,"x":2}}`)) {
		t.Fatal("nested duplicate accepted")
	}
	if !invalidJSONShape([]byte(`[[[[[[[[[1]]]]]]]]]`)) {
		t.Fatal("depth nine accepted")
	}
	if invalidJSONShape([]byte(`{"outer":{"x":1},"next":2}`)) {
		t.Fatal("valid nested object rejected")
	}
}

func TestPublicRenderEveryCommandRejectsTrailingScalars(t *testing.T) {
	valid := map[string]string{"lease": `{"owner_token":"1234567890123456","lease_millis":5000}`, "renew": `{"owner_token":"1234567890123456","job_guid":1,"fence":1,"lease_millis":5000}`, "complete": `{"owner_token":"1234567890123456","job_guid":1,"fence":1}`, "fail": `{"owner_token":"1234567890123456","job_guid":1,"fence":1,"failure":"render_failed"}`, "health": `{}`, "lookup": `{}`}
	for command, body := range valid {
		for _, suffix := range []string{" true", " 0", ` "tail"`, " null"} {
			t.Run(command+suffix, func(t *testing.T) {
				var out, stderr bytes.Buffer
				code := run(context.Background(), []string{command}, strings.NewReader(body+suffix), &out, &stderr, &fakeRenderJobs{})
				if code != 2 || out.Len() != 0 {
					t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
				}
			})
		}
	}
}

func TestPublicRenderDecodeExactAllowsTrailingWhitespace(t *testing.T) {
	var input struct{}
	if !decodeExact([]byte("{}\n\t "), &input) {
		t.Fatal("valid trailing whitespace rejected")
	}
}

func TestPublicRenderCLIMapsLeaseLossToConflictExit(t *testing.T) {
	jobs := &errorRenderJobs{err: service.ErrPublicRenderLeaseLost}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"complete"}, strings.NewReader(`{"owner_token":"owner-one-long-random-token","job_guid":123,"fence":1}`), &out, &stderr, jobs)
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
func (e *errorRenderJobs) Health(context.Context) (service.PublicRenderHealthStatus, error) {
	return service.PublicRenderHealthStatus{}, e.err
}
func (e *errorRenderJobs) Lookup(context.Context) (*service.PublicRenderGeneration, error) {
	return nil, e.err
}
