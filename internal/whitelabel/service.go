package whitelabel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
)

// WhiteLabelService is the single, cached source for permitted upstream model
// metadata. It owns an HTTP client configured by the application, never a
// caller-provided URL or credential.
type WhiteLabelService struct {
	baseURL string
	apiKey  string
	allowed map[string]struct{}
	client  *http.Client
	now     func() time.Time

	mu                sync.Mutex
	catalog           cachedCatalog
	catalogGeneration uint64
	details           map[string]cachedDetail
	detailGenerations map[string]uint64
	disabled          map[string]bool
}

type cachedCatalog struct {
	models    []Model
	fetchedAt time.Time
}

type cachedDetail struct {
	model     Model
	fetchedAt time.Time
}

// NewWhiteLabelService validates all configured model IDs before accepting the
// configuration. Invalid allowlist entries fail closed rather than becoming
// unexpectedly addressable upstream paths.
func NewWhiteLabelService(settings config.WhiteLabelSettings, client *http.Client, now func() time.Time) (*WhiteLabelService, error) {
	if strings.TrimSpace(settings.BaseURL) == "" || strings.TrimSpace(settings.APIKey) == "" {
		return nil, fmt.Errorf("white-label base URL and API key are required")
	}
	if _, err := url.ParseRequestURI(settings.BaseURL); err != nil {
		return nil, fmt.Errorf("white-label base URL: %w", err)
	}
	allowed := make(map[string]struct{}, len(settings.AllowedModels))
	for id := range settings.AllowedModels {
		if !validModelID(id) {
			return nil, fmt.Errorf("invalid configured model ID")
		}
		allowed[id] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("white-label allowlist must not be empty")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	return &WhiteLabelService{baseURL: strings.TrimRight(settings.BaseURL, "/"), apiKey: settings.APIKey, allowed: allowed, client: client, now: now, details: make(map[string]cachedDetail), detailGenerations: make(map[string]uint64), disabled: make(map[string]bool)}, nil
}

// ListModels intersects the fixed service allowlist with the supplied user or
// gateway-token ACL. An empty ACL means unrestricted only inside the service
// allowlist.
func (s *WhiteLabelService) ListModels(ctx context.Context, acl []string) (Catalog, *Error) {
	now := s.now()
	s.mu.Lock()
	cache := s.catalog
	if !cache.fetchedAt.IsZero() && now.Sub(cache.fetchedAt) <= catalogTTL {
		out := s.filteredCatalogLocked(cache.models, acl)
		s.mu.Unlock()
		return Catalog{Data: out}, nil
	}
	s.catalogGeneration++
	refreshGeneration := s.catalogGeneration
	s.mu.Unlock()

	models, fetchErr := s.fetchCatalog(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if fetchErr == nil && refreshGeneration == s.catalogGeneration {
		s.catalog = cachedCatalog{models: models, fetchedAt: now}
		// A successful catalog refresh is the sole recovery route for a model
		// disabled by a trusted detail 404, and only models present can recover.
		present := make(map[string]struct{}, len(models))
		for _, model := range models {
			present[model.ID] = struct{}{}
		}
		for id := range s.disabled {
			if _, ok := present[id]; ok {
				delete(s.disabled, id)
			}
		}
	}
	if fetchErr == nil {
		return Catalog{Data: s.filteredCatalogLocked(models, acl)}, nil
	}
	if !cache.fetchedAt.IsZero() && now.Sub(cache.fetchedAt) <= staleTTL {
		return Catalog{Data: s.filteredCatalogLocked(cache.models, acl), CatalogStale: true}, nil
	}
	return Catalog{}, ErrUpstreamUnavailable("catalog refresh failed")
}

// GetModel authorizes before making any detail request. A direct upstream 404
// from this authenticated client disables the model until a successful catalog
// refresh contains it again.
func (s *WhiteLabelService) GetModel(ctx context.Context, id string, acl []string) (Model, *Error) {
	if !validModelID(id) || !s.permitted(id, acl) {
		return Model{}, &Error{Code: CodeModelUnavailable, Status: http.StatusNotFound, Type: TypeInvalidRequest}
	}
	now := s.now()
	s.mu.Lock()
	if s.disabled[id] {
		s.mu.Unlock()
		return Model{}, &Error{Code: CodeModelUnavailable, Status: http.StatusNotFound, Type: TypeInvalidRequest}
	}
	if cached, ok := s.details[id]; ok && now.Sub(cached.fetchedAt) <= detailTTL {
		model := cached.model
		s.mu.Unlock()
		return model, nil
	}
	s.detailGenerations[id]++
	refreshGeneration := s.detailGenerations[id]
	s.mu.Unlock()

	model, status, fetchErr := s.fetchDetail(ctx, id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if status == http.StatusNotFound {
		if refreshGeneration == s.detailGenerations[id] {
			s.disabled[id] = true
			delete(s.details, id)
		}
		return Model{}, &Error{Code: CodeModelUnavailable, Status: http.StatusNotFound, Type: TypeInvalidRequest}
	}
	if fetchErr != nil {
		return Model{}, ErrUpstreamUnavailable("model detail fetch failed")
	}
	if model.ID != id {
		return Model{}, ErrUpstreamUnavailable("model detail identity mismatch")
	}
	if refreshGeneration == s.detailGenerations[id] {
		s.details[id] = cachedDetail{model: model, fetchedAt: now}
	}
	return model, nil
}

func (s *WhiteLabelService) permitted(id string, acl []string) bool {
	if _, ok := s.allowed[id]; !ok {
		return false
	}
	if len(acl) == 0 {
		return true
	}
	for _, candidate := range acl {
		if candidate == id {
			return true
		}
	}
	return false
}

func (s *WhiteLabelService) filteredCatalogLocked(models []Model, acl []string) []Model {
	out := make([]Model, 0, len(models))
	for _, model := range models {
		if !s.disabled[model.ID] && s.permitted(model.ID, acl) {
			out = append(out, model)
		}
	}
	return cloneModels(out)
}

func (s *WhiteLabelService) fetchCatalog(ctx context.Context) ([]Model, error) {
	request, err := s.newRequest(ctx, s.baseURL+"/models")
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("upstream status %d", response.StatusCode)
	}
	var payload struct {
		Data []upstreamModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	return normalizeModels(payload.Data), nil
}

func (s *WhiteLabelService) fetchDetail(ctx context.Context, id string) (Model, int, error) {
	request, err := s.newRequest(ctx, s.baseURL+"/models/"+url.PathEscape(id))
	if err != nil {
		return Model{}, 0, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return Model{}, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Model{}, response.StatusCode, nil
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Model{}, response.StatusCode, fmt.Errorf("upstream status %d", response.StatusCode)
	}
	var payload upstreamModel
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return Model{}, response.StatusCode, err
	}
	models := normalizeModels([]upstreamModel{payload})
	if len(models) != 1 {
		return Model{}, response.StatusCode, fmt.Errorf("invalid upstream model")
	}
	return models[0], response.StatusCode, nil
}

func (s *WhiteLabelService) newRequest(ctx context.Context, target string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+s.apiKey)
	request.Header.Set("Accept", "application/json")
	return request, nil
}
