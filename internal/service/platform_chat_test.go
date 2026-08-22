package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// The platform compare endpoint must reject an excessive fan-out locally so a
// malformed client request cannot create unbounded upstream work or charges.
func TestValidateCompareModelsRejectsMoreThanThree(t *testing.T) {
	if err := validateCompareModels([]string{"model-a", "model-b", "model-c", "model-d"}); err == nil {
		t.Fatal("expected four compare models to be rejected")
	}
}

func TestValidateCompareModelsAcceptsOneToThreeDistinctModels(t *testing.T) {
	for _, ids := range [][]string{{"model-a"}, {"model-a", "model-b"}, {"model-a", "model-b", "model-c"}} {
		if err := validateCompareModels(ids); err != nil {
			t.Fatalf("ids %#v rejected: %v", ids, err)
		}
	}
}

// Compare streaming is a fan-out: one failed upstream must not cancel the
// other two streams, and the coordinator owns the single terminal [DONE].
func TestCompareWhiteLabelStreamsKeepsOtherModelsRunningAfterOneFails(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model, _ := whitelabel.RequestModelAndStream(body)
		if model == "model-a" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		current := active.Add(1)
		for {
			seen := peak.Load()
			if current <= seen || peak.CompareAndSwap(seen, current) {
				break
			}
		}
		defer active.Add(-1)
		time.Sleep(40 * time.Millisecond)
		_, _ = io.WriteString(w, `data: {"id":"chunk-`+model+`","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"delta":{"content":"`+model+`"},"finish_reason":null}]}`+"\n\n"+"data: [DONE]\n\n")
	}))
	defer upstream.Close()

	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{
		BaseURL: upstream.URL, APIKey: "test-key",
		AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}, "model-c": {}},
	}, upstream.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open("sqlite://"+t.TempDir()+"/platform.db", "test")
	if err != nil {
		t.Fatal(err)
	}
	user := &models.User{Phone: "13900139100", Status: models.UserStatusActive, PlanType: models.PlanEnterprise}
	if err := database.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	platform := NewPlatformChatService(PlatformDeps{WhiteLabel: whiteLabel, Billing: NewBillingService(&config.Settings{})})
	var output strings.Builder
	var outputMu sync.Mutex
	err = platform.CompareStream(context.Background(), database, user, []string{"model-a", "model-b", "model-c"}, ChatParams{
		Messages:       []map[string]interface{}{{"role": "user", "content": "hello"}},
		MaxTokens:      intPtr(5),
		WhiteLabelBody: []byte(`{"max_tokens":5}`),
	}, "req_compare_test", func(frame []byte) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		_, err := output.Write(frame)
		return err
	})
	if err != nil {
		t.Fatalf("CompareStream returned error: %v", err)
	}
	got := output.String()
	for _, want := range []string{`event: model_error`, `"model":"model-a"`, `"model":"model-b"`, `"model":"model-c"`, `event: model_done`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in stream: %s", want, got)
		}
	}
	if count := strings.Count(got, "data: [DONE]"); count != 1 {
		t.Fatalf("terminal [DONE] count=%d stream=%s", count, got)
	}
	if peak.Load() < 2 {
		t.Fatalf("expected concurrent healthy streams, peak=%d", peak.Load())
	}
}

func TestComparePendingModelErrorWriteFailureStopsFurtherFrames(t *testing.T) {
	writeErr := errors.New("client disconnected")
	failed := make(chan struct{})
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		model, _ := whitelabel.RequestModelAndStream(body)
		if model == "model-a" {
			close(failed)
			return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}
		<-failed
		chunk := `data: {"id":"chunk-b","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"delta":{"content":"b"}}]}` + "\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(chunk)), Header: make(http.Header)}, nil
	})
	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://white-label.test/v1", APIKey: "test-key", AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}}}, &http.Client{Transport: transport}, nil)
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open("sqlite://"+t.TempDir()+"/platform.db", "test")
	if err != nil {
		t.Fatal(err)
	}
	user := &models.User{Phone: "13900139101", Status: models.UserStatusActive, PlanType: models.PlanEnterprise}
	if err := database.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	platform := NewPlatformChatService(PlatformDeps{WhiteLabel: whiteLabel, Billing: NewBillingService(&config.Settings{})})
	writes := 0
	err = platform.CompareStream(context.Background(), database, user, []string{"model-a", "model-b"}, ChatParams{Messages: []map[string]interface{}{{"role": "user", "content": "hello"}}, MaxTokens: intPtr(5), WhiteLabelBody: []byte(`{"max_tokens":5}`)}, "req_write_failure", func(frame []byte) error {
		writes++
		if strings.Contains(string(frame), "event: model_error") {
			return writeErr
		}
		return nil
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("error=%v, want write error", err)
	}
	if writes != 1 {
		t.Fatalf("writes=%d, want only failed pending model_error", writes)
	}
}

func intPtr(value int) *int { return &value }
