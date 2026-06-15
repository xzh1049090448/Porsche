package registry

import (
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type ModelRoute struct {
	LogicalName   string
	Provider      string
	UpstreamModel string
	BaseURL       string
	APIKeysEnv    string
}

type ModelRegistry struct {
	mu         sync.RWMutex
	configPath string
	routes     map[string]ModelRoute
	envKeys    map[string]string
}

func NewModelRegistry(configPath string, envKeys map[string]string) *ModelRegistry {
	r := &ModelRegistry{
		configPath: configPath,
		routes:     make(map[string]ModelRoute),
		envKeys:    envKeys,
	}
	r.Load()
	return r
}

func (r *ModelRegistry) Load() {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(r.configPath)
	if err != nil {
		r.routes = make(map[string]ModelRoute)
		return
	}

	var raw struct {
		Routes map[string]map[string]interface{} `yaml:"routes"`
	}
	if yaml.Unmarshal(data, &raw) != nil {
		return
	}

	loaded := make(map[string]ModelRoute)
	for logical, cfg := range raw.Routes {
		name := strings.TrimSpace(logical)
		if name == "" {
			continue
		}
		provider, _ := cfg["provider"].(string)
		upstream, _ := cfg["upstream_model"].(string)
		keysEnv, _ := cfg["api_keys_env"].(string)
		baseURL, _ := cfg["base_url"].(string)
		if provider == "" || upstream == "" || keysEnv == "" {
			continue
		}
		loaded[name] = ModelRoute{
			LogicalName:   name,
			Provider:      strings.TrimSpace(provider),
			UpstreamModel: strings.TrimSpace(upstream),
			BaseURL:       strings.TrimSpace(baseURL),
			APIKeysEnv:    strings.TrimSpace(keysEnv),
		}
	}
	r.routes = loaded
}

func (r *ModelRegistry) Reload(envKeys map[string]string) {
	r.envKeys = envKeys
	r.Load()
}

func (r *ModelRegistry) Routes() map[string]ModelRoute {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]ModelRoute, len(r.routes))
	for k, v := range r.routes {
		out[k] = v
	}
	return out
}

func (r *ModelRegistry) Get(name string) (ModelRoute, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.routes[name]
	return route, ok
}

func (r *ModelRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.routes)
}

func (r *ModelRegistry) KeysForRoute(route ModelRoute) []string {
	raw := r.envKeys[route.APIKeysEnv]
	if raw == "" {
		raw = os.Getenv(route.APIKeysEnv)
	}
	var keys []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	}) {
		if k := strings.TrimSpace(part); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

type ClientConfig struct {
	Name               string   `yaml:"name"`
	Secret             string   `yaml:"secret"`
	AllowedModels      []string `yaml:"allowed_models"`
	RPM                int      `yaml:"rpm"`
	TPM                int      `yaml:"tpm"`
	DailyTokenLimit    int      `yaml:"daily_token_limit"`
	MonthlyTokenLimit  int      `yaml:"monthly_token_limit"`
	IPAllowlist        []string `yaml:"ip_allowlist"`
}

type ClientRegistry struct {
	mu         sync.RWMutex
	configPath string
	bySecret   map[string]ClientConfig
	count      int
}

func NewClientRegistry(configPath string) *ClientRegistry {
	c := &ClientRegistry{
		configPath: configPath,
		bySecret:   make(map[string]ClientConfig),
	}
	c.Load()
	return c
}

func (c *ClientRegistry) Load() {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.configPath)
	if err != nil {
		c.bySecret = make(map[string]ClientConfig)
		c.count = 0
		return
	}

	var raw struct {
		Clients []ClientConfig `yaml:"clients"`
	}
	if yaml.Unmarshal(data, &raw) != nil {
		return
	}

	loaded := make(map[string]ClientConfig)
	for _, client := range raw.Clients {
		if client.Secret != "" {
			loaded[client.Secret] = client
		}
	}
	c.bySecret = loaded
	c.count = len(raw.Clients)
}

func (c *ClientRegistry) Reload() { c.Load() }

func (c *ClientRegistry) ClientCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.count
}

func (c *ClientRegistry) GetBySecret(secret string) (ClientConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	client, ok := c.bySecret[secret]
	return client, ok
}

func IPAllowed(client ClientConfig, host string) bool {
	if len(client.IPAllowlist) == 0 {
		return true
	}
	for _, ip := range client.IPAllowlist {
		if ip == host {
			return true
		}
	}
	return false
}

func ModelAllowed(client ClientConfig, model string) bool {
	if len(client.AllowedModels) == 0 {
		return true
	}
	for _, m := range client.AllowedModels {
		if m == model {
			return true
		}
	}
	return false
}
