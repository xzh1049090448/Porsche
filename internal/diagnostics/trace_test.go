package diagnostics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"runtime/debug"
	"strings"
	"testing"
)

func TestTraceFinalRecordIsBoundedAndWrittenOnce(t *testing.T) {
	var out bytes.Buffer
	ctx, tr := New(context.Background())
	From(ctx).Begin(Auth)(OK)
	From(ctx).Begin(Stage("secret-stage"))(Reason("secret-reason"))
	From(ctx).Mark(UpstreamAttempted)
	tr.End(&out, 503, "request-secret")
	tr.End(&out, 200, "other")
	var r Record
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("request-secret"))
	if r.RequestIDSHA256 != hex.EncodeToString(sum[:]) || r.TraceID == "" || strings.Contains(out.String(), "secret") {
		t.Fatalf("unsafe/incomplete: %s", out.String())
	}
	if r.Stages[Auth].State != "success" || r.Stages[Quota].State != "not_run" || !r.UpstreamRequestAttempted || r.HTTPStatus != 503 {
		t.Fatalf("record=%+v", r)
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatal("duplicate final record")
	}
}
func TestAbsentTraceAndLoggingFailureAreHarmless(t *testing.T) {
	From(context.Background()).Begin(Auth)(OK)
	From(context.Background()).Mark(FirstFrame)
	_, tr := New(context.Background())
	tr.End(badWriter{}, 200, "")
}

type badWriter struct{}

func (badWriter) Write([]byte) (int, error) { return 0, errors.New("secret") }
func TestBuildVersionOnlyAcceptsRevisionAndModified(t *testing.T) {
	rev, modified := buildVersion(&debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: strings.Repeat("a", 40)}, {Key: "vcs.modified", Value: "true"}}})
	if rev != strings.Repeat("a", 40) || modified != "true" {
		t.Fatal(rev, modified)
	}
	rev, modified = buildVersion(&debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "secret-url"}, {Key: "vcs.modified", Value: "secret"}}})
	if rev != "unknown" || modified != "unknown" {
		t.Fatal(rev, modified)
	}
}

func TestNetworkReasonsUseTypesAndIDFallbackIsUnique(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want Reason
	}{{&net.DNSError{Err: "SENSITIVE", Name: "SENSITIVE"}, DNS}, {&tls.CertificateVerificationError{Err: errors.New("SENSITIVE")}, TLS}, {&net.OpError{Op: "dial", Err: errors.New("SENSITIVE")}, Connection}, {context.DeadlineExceeded, Timeout}} {
		if got := NetworkReason(tc.err); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
	a, b := newTraceID(badReader{}), newTraceID(badReader{})
	if a == b || a == strings.Repeat("0", 32) || len(a) != 32 {
		t.Fatal("fallback must preserve per-request ID")
	}
}

type badReader struct{}

func (badReader) Read([]byte) (int, error) { return 0, errors.New("SENSITIVE") }
