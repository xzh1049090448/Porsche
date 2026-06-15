package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/registry"
)

type UpstreamKeyEntry struct {
	Secret string
	Index  int
}

type KeyPool struct {
	mu      sync.Mutex
	keys    map[string][]UpstreamKeyEntry
	cursor  map[string]int
	failures map[string]int
	openUntil map[string]time.Time
	settings  *config.Settings
}

func NewKeyPool(settings *config.Settings) *KeyPool {
	return &KeyPool{
		keys:      make(map[string][]UpstreamKeyEntry),
		cursor:    make(map[string]int),
		failures:  make(map[string]int),
		openUntil: make(map[string]time.Time),
		settings:  settings,
	}
}

func (p *KeyPool) Rebuild(models *registry.ModelRegistry) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.keys = make(map[string][]UpstreamKeyEntry)
	for name, route := range models.Routes() {
		rawKeys := models.KeysForRoute(route)
		var entries []UpstreamKeyEntry
		for i, k := range rawKeys {
			entries = append(entries, UpstreamKeyEntry{Secret: k, Index: i})
		}
		p.keys[name] = entries
	}
}

func (p *KeyPool) NextKey(model string) *UpstreamKeyEntry {
	p.mu.Lock()
	defer p.mu.Unlock()

	if until, ok := p.openUntil[model]; ok && time.Now().Before(until) {
		return nil
	}

	keys := p.keys[model]
	if len(keys) == 0 {
		return nil
	}
	idx := p.cursor[model] % len(keys)
	p.cursor[model] = idx + 1
	entry := keys[idx]
	return &entry
}

func (p *KeyPool) ReportSuccess(model string, _ UpstreamKeyEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures[model] = 0
	delete(p.openUntil, model)
}

func (p *KeyPool) ReportFailure(model string, tripped bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures[model]++
	if tripped && p.failures[model] >= p.settings.CircuitFailureThreshold {
		p.openUntil[model] = time.Now().Add(time.Duration(p.settings.CircuitOpenSeconds) * time.Second)
	}
}

type ChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

type Service struct {
	settings *config.Settings
	models   *registry.ModelRegistry
	pool     *KeyPool
	client   *http.Client
}

func NewService(settings *config.Settings, models *registry.ModelRegistry, pool *KeyPool) *Service {
	return &Service{
		settings: settings,
		models:   models,
		pool:     pool,
		client: &http.Client{
			Timeout: time.Duration(settings.UpstreamTimeoutSeconds * float64(time.Second)),
		},
	}
}

func (s *Service) Complete(ctx context.Context, client registry.ClientConfig, body ChatCompletionRequest) (map[string]interface{}, error) {
	logicalModel := body.Model
	if !registry.ModelAllowed(client, logicalModel) {
		return nil, fmt.Errorf("forbidden: model not allowed")
	}

	route, ok := s.models.Get(logicalModel)
	if !ok {
		return nil, fmt.Errorf("unknown model: %s", logicalModel)
	}

	if route.Provider != "openai_compatible" {
		return nil, fmt.Errorf("unsupported provider: %s", route.Provider)
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		key := s.pool.NextKey(logicalModel)
		if key == nil {
			return nil, fmt.Errorf("no upstream keys for model %s (configure %s)", logicalModel, route.APIKeysEnv)
		}

		payload := map[string]interface{}{
			"model":    route.UpstreamModel,
			"messages": body.Messages,
			"stream":   false,
		}
		if body.Temperature != nil {
			payload["temperature"] = *body.Temperature
		}
		if body.MaxTokens != nil {
			payload["max_tokens"] = *body.MaxTokens
		}

		status, data, err := s.forwardJSON(ctx, route.BaseURL+"/chat/completions", key.Secret, payload)
		if err != nil {
			lastErr = err
			s.pool.ReportFailure(logicalModel, true)
			time.Sleep(time.Duration(min(2<<attempt, 10)+rand.Intn(1000)/1000) * time.Second)
			continue
		}
		if status >= 400 {
			lastErr = fmt.Errorf("upstream status %d", status)
			s.pool.ReportFailure(logicalModel, status == 401 || status == 403 || status == 429 || status >= 500)
			if status == 400 || status == 404 || attempt >= 3 {
				return nil, lastErr
			}
			time.Sleep(time.Duration(min(2<<attempt, 10)) * time.Second)
			continue
		}

		s.pool.ReportSuccess(logicalModel, *key)
		if data != nil {
			data["model"] = logicalModel
		}
		return data, nil
	}
	return nil, lastErr
}

func (s *Service) Stream(ctx context.Context, client registry.ClientConfig, body ChatCompletionRequest) (*http.Response, error) {
	logicalModel := body.Model
	if !registry.ModelAllowed(client, logicalModel) {
		return nil, fmt.Errorf("forbidden")
	}
	route, ok := s.models.Get(logicalModel)
	if !ok {
		return nil, fmt.Errorf("unknown model")
	}
	key := s.pool.NextKey(logicalModel)
	if key == nil {
		return nil, fmt.Errorf("no upstream keys")
	}

	payload := map[string]interface{}{
		"model":    route.UpstreamModel,
		"messages": body.Messages,
		"stream":   true,
	}
	if body.Temperature != nil {
		payload["temperature"] = *body.Temperature
	}
	if body.MaxTokens != nil {
		payload["max_tokens"] = *body.MaxTokens
	}

	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, route.BaseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := s.client.Do(req)
	if err != nil {
		s.pool.ReportFailure(logicalModel, true)
		return nil, err
	}
	if resp.StatusCode >= 400 {
		s.pool.ReportFailure(logicalModel, true)
	}
	return resp, nil
}

func (s *Service) forwardJSON(ctx context.Context, url, apiKey string, payload map[string]interface{}) (int, map[string]interface{}, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, nil, fmt.Errorf("%s", strings.TrimSpace(string(raw)))
	}
	var data map[string]interface{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &data)
	}
	return resp.StatusCode, data, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
