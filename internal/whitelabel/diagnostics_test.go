package whitelabel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/diagnostics"
)

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func diagnosticRecord(t *testing.T, tr *diagnostics.Trace) diagnostics.Record {
	t.Helper()
	var b bytes.Buffer
	tr.End(&b, 503, "secret-id")
	if strings.Contains(b.String(), "SENSITIVE") {
		t.Fatal("sensitive diagnostic")
	}
	var r diagnostics.Record
	if err := json.Unmarshal(b.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestDiagnosticChatClassifiesTransportWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		err    error
		reason diagnostics.Reason
	}{{"timeout", 0, context.DeadlineExceeded, diagnostics.Timeout}, {"cancel", 0, context.Canceled, diagnostics.Canceled}, {"network", 0, errors.New("SENSITIVE"), diagnostics.Network}, {"429", 429, nil, diagnostics.Non2xx}, {"500", 500, nil, diagnostics.Non2xx}, {"ok", 200, nil, diagnostics.OK}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s, err := NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://SENSITIVE.test/v1", APIKey: "SENSITIVE", AllowedModels: map[string]struct{}{"model-a": {}}}, &http.Client{Transport: diagnosticTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("SENSITIVE")), Header: make(http.Header)}, nil
			})}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, tr := diagnostics.New(context.Background())
			resp, e := s.Chat(ctx, []byte(`{"messages":"SENSITIVE"}`))
			if resp != nil {
				resp.Body.Close()
			}
			if (e == nil) != (tc.reason == diagnostics.OK) {
				t.Fatal("public result changed")
			}
			r := diagnosticRecord(t, tr)
			if r.UpstreamHTTPStatus != tc.status {
				t.Fatalf("upstream status=%d want=%d", r.UpstreamHTTPStatus, tc.status)
			}
			if calls != 1 || r.Stages[diagnostics.Connect].Reason != tc.reason || !r.UpstreamRequestAttempted || r.UpstreamResponseReceived != (tc.err == nil) {
				t.Fatalf("calls=%d record=%+v", calls, r)
			}
		})
	}
}
func TestDiagnosticSSEClassifiesMalformedEOFAndWrite(t *testing.T) {
	chunk := "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":0,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"SENSITIVE\"}}]}\n\n"
	for _, tc := range []struct {
		name, body string
		writeErr   error
		reason     diagnostics.Reason
	}{{"malformed", "data: SENSITIVE\n\n", nil, diagnostics.Malformed}, {"early_eof", chunk, nil, diagnostics.Incomplete}, {"write", chunk, errors.New("SENSITIVE"), diagnostics.Write}, {"done", chunk + "data: [DONE]\n\n", nil, diagnostics.OK}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, tr := diagnostics.New(context.Background())
			s := &WhiteLabelService{}
			e := s.ProjectChatCompletionSSEContext(ctx, strings.NewReader(tc.body), "model-a", func([]byte) error { return tc.writeErr })
			if (e == nil) != (tc.reason == diagnostics.OK) {
				t.Fatal("public result changed")
			}
			r := diagnosticRecord(t, tr)
			if r.Stages[diagnostics.Stream].Reason != tc.reason {
				t.Fatalf("record=%+v", r)
			}
		})
	}
}

func TestDiagnosticSSEBodyErrorsWithoutCanceledParent(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want diagnostics.Reason
	}{{"timeout", diagnosticTimeoutError{}, diagnostics.Timeout}, {"canceled", context.Canceled, diagnostics.Canceled}, {"unknown", errors.New("SENSITIVE"), diagnostics.Read}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, tr := diagnostics.New(context.Background())
			s := &WhiteLabelService{}
			err := s.ProjectChatCompletionSSEContext(ctx, diagnosticErrorReader{tc.err}, "model-a", func([]byte) error { t.Fatal("unexpected frame"); return nil })
			if err == nil {
				t.Fatal("expected existing public error")
			}
			r := diagnosticRecord(t, tr)
			if r.Stages[diagnostics.Stream].Reason != tc.want {
				t.Fatalf("got %s want %s", r.Stages[diagnostics.Stream].Reason, tc.want)
			}
		})
	}
}

type diagnosticErrorReader struct{ err error }

func (r diagnosticErrorReader) Read([]byte) (int, error) { return 0, r.err }

type diagnosticTimeoutError struct{}

func (diagnosticTimeoutError) Error() string   { return "SENSITIVE timeout" }
func (diagnosticTimeoutError) Timeout() bool   { return true }
func (diagnosticTimeoutError) Temporary() bool { return true }

func TestDiagnosticRedirectResponseWithError(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: diagnosticTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"/SENSITIVE"}}, Body: io.NopCloser(strings.NewReader("SENSITIVE")), Request: r}, nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("SENSITIVE") }}
	s, e := NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://SENSITIVE.test/v1", APIKey: "SENSITIVE", AllowedModels: map[string]struct{}{"model-a": {}}}, client, nil)
	if e != nil {
		t.Fatal(e)
	}
	ctx, tr := diagnostics.New(context.Background())
	resp, err := s.Chat(ctx, []byte(`{"model":"model-a"}`))
	if resp != nil || err == nil || err.Status != 503 || calls != 1 {
		t.Fatal("public behavior/replay changed")
	}
	r := diagnosticRecord(t, tr)
	if r.Stages[diagnostics.Connect].Reason != diagnostics.RedirectRejected {
		t.Fatalf("reason=%s", r.Stages[diagnostics.Connect].Reason)
	}
	if !r.UpstreamResponseReceived || r.UpstreamHTTPStatus != 302 {
		t.Fatalf("received=%v status=%d; want true/302", r.UpstreamResponseReceived, r.UpstreamHTTPStatus)
	}
}
