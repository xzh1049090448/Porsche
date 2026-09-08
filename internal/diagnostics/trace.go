// Package diagnostics records only fixed, content-free platform chat diagnostics.
package diagnostics

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

type Stage string

const (
	Auth          Stage = "authentication"
	Validation    Stage = "validation"
	Catalog       Stage = "model_catalog_acl"
	Quota         Stage = "quota_persist"
	Conversation  Stage = "conversation_lookup_create"
	UserMessage   Stage = "user_message_save"
	Title         Stage = "title_save"
	Serialization Stage = "serialization"
	Connect       Stage = "upstream_connect"
	Stream        Stage = "sse_stream"
	Assistant     Stage = "assistant_save"
	Usage         Stage = "usage_save"
	FinalWrite    Stage = "final_write"
)

var stages = []Stage{Auth, Validation, Catalog, Quota, Conversation, UserMessage, Title, Serialization, Connect, Stream, Assistant, Usage, FinalWrite}

type Reason string

const (
	OK               Reason = "none"
	Rejected         Reason = "rejected"
	Database         Reason = "database_error"
	Invalid          Reason = "invalid_request"
	Network          Reason = "network_error"
	DNS              Reason = "dns_error"
	TLS              Reason = "tls_error"
	Connection       Reason = "connection_error"
	Timeout          Reason = "timeout"
	Canceled         Reason = "canceled"
	Non2xx           Reason = "upstream_non_2xx"
	RedirectRejected Reason = "redirect_rejected"
	Malformed        Reason = "malformed_chunk"
	Incomplete       Reason = "early_eof"
	Read             Reason = "stream_read_error"
	Write            Reason = "client_write_error"
	Unavailable      Reason = "unavailable"
)

func safeReason(r Reason) Reason {
	switch r {
	case RedirectRejected, DNS, TLS, Connection, OK, Rejected, Database, Invalid, Network, Timeout, Canceled, Non2xx, Malformed, Incomplete, Read, Write, Unavailable:
		return r
	}
	return Unavailable
}
func NetworkReason(err error) Reason {
	if errors.Is(err, context.Canceled) {
		return Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Timeout
	}
	var n net.Error
	if errors.As(err, &n) && n.Timeout() {
		return Timeout
	}

	var dns *net.DNSError
	if errors.As(err, &dns) {
		return DNS
	}
	var verify *tls.CertificateVerificationError
	var cert x509.CertificateInvalidError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var record tls.RecordHeaderError
	if errors.As(err, &verify) || errors.As(err, &cert) || errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &record) {
		return TLS
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return Connection
	}
	return Network
}

type Flag int

const (
	UpstreamAttempted Flag = iota
	UpstreamReceived
	FirstFrame
	DailyCallSaved
	ConversationReady
	UserMessageSaved
	FinalSaved
)

type StageResult struct {
	State      string  `json:"state"`
	Reason     Reason  `json:"reason"`
	DurationMS float64 `json:"duration_ms"`
}
type Record struct {
	MalformedChunkDetail     *ChunkFailure         `json:"malformed_chunk_detail,omitempty"`
	Event                    string                `json:"event"`
	TraceID                  string                `json:"trace_id"`
	RequestIDSHA256          string                `json:"request_id_sha256"`
	UTC                      string                `json:"utc"`
	Route                    string                `json:"route"`
	Method                   string                `json:"method"`
	VCSRevision              string                `json:"vcs_revision"`
	VCSModified              string                `json:"vcs_modified"`
	DurationMS               float64               `json:"duration_ms"`
	HTTPStatus               int                   `json:"http_status"`
	UpstreamHTTPStatus       int                   `json:"upstream_http_status"`
	Stages                   map[Stage]StageResult `json:"stages"`
	UpstreamRequestAttempted bool                  `json:"upstream_request_attempted"`
	UpstreamResponseReceived bool                  `json:"upstream_response_received"`
	FirstFrameEmitted        bool                  `json:"first_frame_emitted"`
	DailyCallSaved           bool                  `json:"daily_call_saved"`
	ConversationReady        bool                  `json:"conversation_ready"`
	// False means the complete AddMessage operation was not confirmed; an insert may have succeeded before its conversation update failed.
	UserMessageSaved bool `json:"user_message_saved"`
	FinalSaved       bool `json:"final_saved"`
}
type Trace struct {
	mu      sync.Mutex
	once    sync.Once
	started time.Time
	record  Record
}
type contextKey struct{}

func New(ctx context.Context) (context.Context, *Trace) {
	id := newTraceID(rand.Reader)
	revision, modified := "unknown", "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		revision, modified = buildVersion(info)
	}
	t := &Trace{started: time.Now(), record: Record{Event: "platform_chat_diagnostic", TraceID: id, Route: "/api/v1/platform/chat/completions", Method: "POST", VCSRevision: revision, VCSModified: modified, Stages: make(map[Stage]StageResult)}}
	for _, s := range stages {
		t.record.Stages[s] = StageResult{State: "not_run", Reason: OK}
	}
	return context.WithValue(ctx, contextKey{}, t), t
}
func From(ctx context.Context) *Trace { t, _ := ctx.Value(contextKey{}).(*Trace); return t }
func (t *Trace) Begin(s Stage) func(Reason) {
	if t == nil {
		return func(Reason) {}
	}
	t.mu.Lock()
	_, ok := t.record.Stages[s]
	t.mu.Unlock()
	if !ok {
		return func(Reason) {}
	}
	start := time.Now()
	return func(reason Reason) {
		t.mu.Lock()
		defer t.mu.Unlock()
		state := "success"
		reason = safeReason(reason)
		if reason != OK {
			state = "failed"
		}
		t.record.Stages[s] = StageResult{State: state, Reason: reason, DurationMS: float64(time.Since(start)) / float64(time.Millisecond)}
	}
}
func (t *Trace) Mark(f Flag) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch f {
	case UpstreamAttempted:
		t.record.UpstreamRequestAttempted = true
	case UpstreamReceived:
		t.record.UpstreamResponseReceived = true
	case FirstFrame:
		t.record.FirstFrameEmitted = true
	case DailyCallSaved:
		t.record.DailyCallSaved = true
	case ConversationReady:
		t.record.ConversationReady = true
	case UserMessageSaved:
		t.record.UserMessageSaved = true
	case FinalSaved:
		t.record.FinalSaved = true
	}
}

// End deliberately ignores writer errors; diagnostics never change the response.
func (t *Trace) End(w io.Writer, status int, requestID string) {
	if t == nil {
		return
	}
	t.once.Do(func() {
		defer func() { _ = recover() }()
		t.mu.Lock()
		defer t.mu.Unlock()
		sum := sha256.Sum256([]byte(requestID))
		t.record.RequestIDSHA256 = hex.EncodeToString(sum[:])
		t.record.UTC = time.Now().UTC().Format(time.RFC3339Nano)
		t.record.DurationMS = float64(time.Since(t.started)) / float64(time.Millisecond)
		t.record.HTTPStatus = status
		b, err := json.Marshal(t.record)
		if err == nil {
			_, _ = w.Write(append(b, '\n'))
		}
	})
}
func buildVersion(info *debug.BuildInfo) (string, string) {
	rev, modified := "unknown", "unknown"
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) == 40 || len(s.Value) == 64 {
				if b, err := hex.DecodeString(s.Value); err == nil {
					rev = hex.EncodeToString(b)
				}
			}
		case "vcs.modified":
			if s.Value == "true" || s.Value == "false" {
				modified = s.Value
			}
		}
	}
	return rev, modified
}

var fallbackSequence atomic.Uint64

func newTraceID(source io.Reader) string {
	b := make([]byte, 16)
	if _, err := io.ReadFull(source, b); err == nil {
		return hex.EncodeToString(b)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", time.Now().UnixNano(), fallbackSequence.Add(1))))
	return hex.EncodeToString(sum[:16])
}
func (t *Trace) UpstreamStatus(status int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if status >= 100 && status <= 599 {
		t.record.UpstreamHTTPStatus = status
	}
}
