package whitelabel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
)

func TestCatalogUsesStaleFor24HoursAndTrusted404DisablesModel(t *testing.T) {
	up := newWhiteLabelServer(t, []map[string]any{{"id": "model-a"}})
	clock := &testClock{now: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)}
	svc := newService(t, up.URL(), clock)

	catalog, err := svc.ListModels(context.Background(), nil)
	requireNoServiceError(t, err)
	requireModelIDs(t, catalog.Data, "model-a")

	up.setCatalogStatus(http.StatusBadGateway)
	clock.Add(23 * time.Hour)
	catalog, err = svc.ListModels(context.Background(), nil)
	requireNoServiceError(t, err)
	if !catalog.CatalogStale {
		t.Fatal("catalog should be marked stale when its refresh fails within 24 hours")
	}
	requireModelIDs(t, catalog.Data, "model-a")

	up.setDetailStatus("model-a", http.StatusNotFound)
	_, err = svc.GetModel(context.Background(), "model-a", nil)
	requireServiceCode(t, err, CodeModelUnavailable)
	catalog, err = svc.ListModels(context.Background(), nil)
	requireNoServiceError(t, err)
	requireModelIDs(t, catalog.Data)
}

func TestCatalogColdFailureAndExpiredStaleReturnUnavailable(t *testing.T) {
	up := newWhiteLabelServer(t, []map[string]any{{"id": "model-a"}})
	clock := &testClock{now: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)}
	cold := newService(t, up.URL(), clock)
	up.setCatalogStatus(http.StatusBadGateway)
	_, err := cold.ListModels(context.Background(), nil)
	requireServiceCode(t, err, CodeGatewayUpstreamUnavailable)

	up.setCatalogStatus(http.StatusOK)
	svc := newService(t, up.URL(), clock)
	if _, err := svc.ListModels(context.Background(), nil); err != nil {
		t.Fatalf("prime catalog: %v", err)
	}
	up.setCatalogStatus(http.StatusBadGateway)
	clock.Add(24*time.Hour + time.Second)
	_, err = svc.ListModels(context.Background(), nil)
	requireServiceCode(t, err, CodeGatewayUpstreamUnavailable)
}

func TestModelACLIntersectsConfiguredAllowlist(t *testing.T) {
	up := newWhiteLabelServer(t, []map[string]any{{"id": "configured"}, {"id": "other"}})
	clock := &testClock{now: time.Now().UTC()}
	svc := newService(t, up.URL(), clock)

	catalog, err := svc.ListModels(context.Background(), []string{"configured", "other"})
	requireNoServiceError(t, err)
	requireModelIDs(t, catalog.Data, "configured")

	catalog, err = svc.ListModels(context.Background(), []string{"other"})
	requireNoServiceError(t, err)
	requireModelIDs(t, catalog.Data)
	_, err = svc.GetModel(context.Background(), "other", []string{"other"})
	requireServiceCode(t, err, CodeModelUnavailable)
}

func TestDetailTransportFailureDoesNotDisableModelOrLeakStaleCatalog(t *testing.T) {
	up := newWhiteLabelServer(t, []map[string]any{{"id": "model-a"}})
	clock := &testClock{now: time.Now().UTC()}
	svc := newService(t, up.URL(), clock)
	if _, err := svc.ListModels(context.Background(), nil); err != nil {
		t.Fatalf("prime catalog: %v", err)
	}
	up.setDetailStatus("model-a", http.StatusBadGateway)
	_, err := svc.GetModel(context.Background(), "model-a", nil)
	requireServiceCode(t, err, CodeGatewayUpstreamUnavailable)
	catalog, err := svc.ListModels(context.Background(), nil)
	requireNoServiceError(t, err)
	requireModelIDs(t, catalog.Data, "model-a")
}

func TestDetailCacheExpiresAfterOneHour(t *testing.T) {
	up := newWhiteLabelServer(t, []map[string]any{{"id": "model-a"}})
	clock := &testClock{now: time.Now().UTC()}
	svc := newService(t, up.URL(), clock)
	if _, err := svc.GetModel(context.Background(), "model-a", nil); err != nil {
		t.Fatalf("prime detail: %v", err)
	}
	up.setDetailStatus("model-a", http.StatusBadGateway)
	clock.Add(detailTTL - time.Second)
	if _, err := svc.GetModel(context.Background(), "model-a", nil); err != nil {
		t.Fatalf("fresh detail cache should be used: %v", err)
	}
	clock.Add(2 * time.Second)
	_, err := svc.GetModel(context.Background(), "model-a", nil)
	requireServiceCode(t, err, CodeGatewayUpstreamUnavailable)
}

func TestSuccessfulCatalogRefreshReenablesOnlyModelsItContains(t *testing.T) {
	up := newWhiteLabelServer(t, []map[string]any{{"id": "model-a"}})
	clock := &testClock{now: time.Now().UTC()}
	svc := newService(t, up.URL(), clock)
	if _, err := svc.ListModels(context.Background(), nil); err != nil {
		t.Fatalf("prime catalog: %v", err)
	}
	up.setDetailStatus("model-a", http.StatusNotFound)
	_, err := svc.GetModel(context.Background(), "model-a", nil)
	requireServiceCode(t, err, CodeModelUnavailable)
	clock.Add(catalogTTL + time.Second)
	if _, err := svc.ListModels(context.Background(), nil); err != nil {
		t.Fatalf("refresh catalog: %v", err)
	}
	up.setDetailStatus("model-a", http.StatusOK)
	if _, err := svc.GetModel(context.Background(), "model-a", nil); err != nil {
		t.Fatalf("catalog refresh did not re-enable listed model: %v", err)
	}
}

func TestInvalidOrDeniedIDDoesNotCallDetailUpstream(t *testing.T) {
	up := newWhiteLabelServer(t, []map[string]any{{"id": "model-a"}})
	svc := newService(t, up.URL(), &testClock{now: time.Now().UTC()})
	for _, id := range []string{"model-a", "", " model-a", "a/b", "a%2fb", "a?b", "a#b"} {
		acl := []string{"other"}
		if id != "model-a" {
			acl = nil
		}
		_, err := svc.GetModel(context.Background(), id, acl)
		requireServiceCode(t, err, CodeModelUnavailable)
	}
	if got := up.lastDetailEscapedPath(); got != "" {
		t.Fatalf("denied ID made detail upstream request: %q", got)
	}
}

func TestCatalogAbsentModelDoesNotCallDetailUpstream(t *testing.T) {
	up := newWhiteLabelServer(t, []map[string]any{{"id": "other"}})
	svc := newServiceWithAllowed(t, up.URL(), &testClock{now: time.Now().UTC()}, "model-a", "other")

	_, err := svc.GetModel(context.Background(), "model-a", nil)
	requireServiceCode(t, err, CodeModelUnavailable)
	if got := up.detailCalls(); got != 0 {
		t.Fatalf("catalog-absent model made %d detail upstream calls, want 0", got)
	}
}

func TestNewWhiteLabelServiceRejectsUnsafeConfiguredIDs(t *testing.T) {
	_, err := NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://example.test/v1", APIKey: "key", AllowedModels: map[string]struct{}{"a/b": {}}}, &http.Client{}, time.Now)
	if err == nil {
		t.Fatal("unsafe configured model ID was accepted")
	}
}

func TestServiceSanitizesMetadataAndEscapesOpaqueDetailIDOnce(t *testing.T) {
	const opaqueID = "vendor:model@2026.08"
	up := newWhiteLabelServer(t, []map[string]any{
		{"id": "", "input_token_price_per_m": -2},
		{"id": opaqueID, "input_token_price_per_m": -1, "output_token_price_per_m": 2.5, "context_window": -1, "max_tokens": 16},
	})
	clock := &testClock{now: time.Now().UTC()}
	svc := newService(t, up.URL(), clock)
	catalog, err := svc.ListModels(context.Background(), nil)
	requireNoServiceError(t, err)
	requireModelIDs(t, catalog.Data, opaqueID)
	model := catalog.Data[0]
	if model.InputTokenPricePerM != 0 || model.ContextWindow != 0 || model.OutputTokenPricePerM != 2.5 || model.MaxTokens != 16 {
		t.Fatalf("metadata was not sanitized: %#v", model)
	}
	if _, err := svc.GetModel(context.Background(), opaqueID, nil); err != nil {
		t.Fatalf("GetModel() error = %v", err)
	}
	if got := up.lastDetailEscapedPath(); got != "/models/vendor:model@2026.08" {
		t.Fatalf("detail path = %q, want ID encoded once", got)
	}
}

func TestCatalogOlderRefreshCannotOverwriteNewerCache(t *testing.T) {
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	var requests struct {
		sync.Mutex
		count int
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requests.Lock()
		requests.count++
		request := requests.count
		requests.Unlock()
		if request == 1 {
			close(oldStarted)
			<-releaseOld
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "model-old"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "model-new"}}})
	}))
	t.Cleanup(up.Close)
	clock := &testClock{now: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)}
	svc := newServiceWithAllowed(t, up.URL, clock, "model-old", "model-new")

	oldResult := make(chan *Error, 1)
	go func() { _, err := svc.ListModels(context.Background(), nil); oldResult <- err }()
	<-oldStarted
	clock.Add(time.Second)
	if _, err := svc.ListModels(context.Background(), nil); err != nil {
		t.Fatalf("newer catalog refresh: %v", err)
	}
	close(releaseOld)
	if err := <-oldResult; err != nil {
		t.Fatalf("older catalog refresh: %v", err)
	}

	catalog, err := svc.ListModels(context.Background(), nil)
	requireNoServiceError(t, err)
	requireModelIDs(t, catalog.Data, "model-new")
}

func TestDetailOlderRefreshCannotOverwriteNewerCache(t *testing.T) {
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	var requests struct {
		sync.Mutex
		count int
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GetModel now verifies catalog membership before fetching a detail.
		// Keep that gate realistic but cached, so the two detail fetches below
		// still exercise their intended concurrent-refresh ordering.
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "model-a"}}})
			return
		}
		if r.URL.Path != "/models/model-a" {
			t.Errorf("path = %q, want /models/model-a", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requests.Lock()
		requests.count++
		request := requests.count
		requests.Unlock()
		if request == 1 {
			close(oldStarted)
			<-releaseOld
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "model-a", "title": "old"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "model-a", "title": "new"})
	}))
	t.Cleanup(up.Close)
	clock := &testClock{now: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)}
	svc := newServiceWithAllowed(t, up.URL, clock, "model-a")

	oldResult := make(chan *Error, 1)
	go func() { _, err := svc.GetModel(context.Background(), "model-a", nil); oldResult <- err }()
	<-oldStarted
	clock.Add(time.Second)
	if _, err := svc.GetModel(context.Background(), "model-a", nil); err != nil {
		t.Fatalf("newer detail refresh: %v", err)
	}
	close(releaseOld)
	if err := <-oldResult; err != nil {
		t.Fatalf("older detail refresh: %v", err)
	}

	model, err := svc.GetModel(context.Background(), "model-a", nil)
	requireNoServiceError(t, err)
	if model.Title != "new" {
		t.Fatalf("cached detail title = %q, want newer response", model.Title)
	}
}

func TestDetailOlder404CannotDisableNewerCachedModel(t *testing.T) {
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	var requests struct {
		sync.Mutex
		count int
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GetModel now verifies catalog membership before fetching a detail.
		// Keep that gate realistic but cached, so the two detail fetches below
		// still exercise their intended concurrent-refresh ordering.
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "model-a"}}})
			return
		}
		if r.URL.Path != "/models/model-a" {
			t.Errorf("path = %q, want /models/model-a", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requests.Lock()
		requests.count++
		request := requests.count
		requests.Unlock()
		if request == 1 {
			close(oldStarted)
			<-releaseOld
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "model-a", "title": "new"})
	}))
	t.Cleanup(up.Close)
	clock := &testClock{now: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)}
	svc := newServiceWithAllowed(t, up.URL, clock, "model-a")

	oldResult := make(chan *Error, 1)
	go func() { _, err := svc.GetModel(context.Background(), "model-a", nil); oldResult <- err }()
	<-oldStarted
	clock.Add(time.Second)
	if _, err := svc.GetModel(context.Background(), "model-a", nil); err != nil {
		t.Fatalf("newer detail refresh: %v", err)
	}
	close(releaseOld)
	requireServiceCode(t, <-oldResult, CodeModelUnavailable)

	model, err := svc.GetModel(context.Background(), "model-a", nil)
	requireNoServiceError(t, err)
	if model.Title != "new" {
		t.Fatalf("cached detail title = %q, want newer response", model.Title)
	}
	svc.mu.Lock()
	disabled := svc.disabled["model-a"]
	svc.mu.Unlock()
	if disabled {
		t.Fatal("older 404 disabled the model after a newer refresh succeeded")
	}
}

func TestModelMetadataTextIsBounded(t *testing.T) {
	withinLimit := strings.Repeat("x", maxModelMetadataTextBytes)
	overLimit := strings.Repeat("x", maxModelMetadataTextBytes+1)
	models := normalizeModels([]upstreamModel{
		{ID: "model-a", Title: withinLimit, Description: withinLimit},
		{ID: "model-b", Title: overLimit, Description: overLimit},
	})
	if len(models) != 2 {
		t.Fatalf("normalized models = %d, want 2", len(models))
	}
	if models[0].Title != withinLimit || models[0].Description != withinLimit {
		t.Fatal("metadata at the limit was not retained")
	}
	if models[1].Title != "" || models[1].Description != "" {
		t.Fatal("oversized metadata was retained")
	}
}

func newService(t *testing.T, baseURL string, clock *testClock) *WhiteLabelService {
	t.Helper()
	return newServiceWithAllowed(t, baseURL, clock, "model-a", "configured", "vendor:model@2026.08")
}

func newServiceWithAllowed(t *testing.T, baseURL string, clock *testClock, allowedIDs ...string) *WhiteLabelService {
	t.Helper()
	allowed := make(map[string]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = struct{}{}
	}
	svc, err := NewWhiteLabelService(config.WhiteLabelSettings{
		BaseURL:       baseURL,
		APIKey:        "test-key",
		AllowedModels: allowed,
	}, &http.Client{}, clock.Now)
	if err != nil {
		t.Fatalf("NewWhiteLabelService() error = %v", err)
	}
	return svc
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

type whiteLabelServer struct {
	t             *testing.T
	server        *httptest.Server
	mu            sync.Mutex
	catalog       []map[string]any
	catalogStatus int
	detailStatus  map[string]int
	detailEscaped string
	detailCount   int
}

func newWhiteLabelServer(t *testing.T, catalog []map[string]any) *whiteLabelServer {
	t.Helper()
	up := &whiteLabelServer{t: t, catalog: catalog, catalogStatus: http.StatusOK, detailStatus: map[string]int{}}
	up.server = httptest.NewServer(http.HandlerFunc(up.serveHTTP))
	t.Cleanup(up.server.Close)
	return up
}

func (s *whiteLabelServer) URL() string { return s.server.URL }
func (s *whiteLabelServer) setCatalogStatus(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalogStatus = status
}
func (s *whiteLabelServer) setDetailStatus(id string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detailStatus[id] = status
}
func (s *whiteLabelServer) lastDetailEscapedPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detailEscaped
}
func (s *whiteLabelServer) detailCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detailCount
}

func (s *whiteLabelServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-key" {
		s.t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/models" {
		w.WriteHeader(s.catalogStatus)
		if s.catalogStatus == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": s.catalog})
		}
		return
	}
	s.detailEscaped = r.URL.EscapedPath()
	s.detailCount++
	id := r.URL.Path[len("/models/"):]
	status := s.detailStatus[id]
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if status == http.StatusOK {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
	}
}

func requireNoServiceError(t *testing.T, err *Error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %#v", err)
	}
}
func requireServiceCode(t *testing.T, err *Error, want Code) {
	t.Helper()
	if err == nil || err.Code != want {
		t.Fatalf("error = %#v, want %q", err, want)
	}
}
func requireModelIDs(t *testing.T, models []Model, want ...string) {
	t.Helper()
	got := make([]string, len(models))
	for i, model := range models {
		got[i] = model.ID
	}
	if len(got) != len(want) {
		t.Fatalf("model IDs = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("model IDs = %q, want %q", got, want)
		}
	}
}
