package whitelabel

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
)

func TestCatalogObservationReturnsExactSanitizedFreshCatalog(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("authorization=%q", got)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":" alpha/model ","owned_by":"Provider","input_token_price_per_m":0.12345678,"output_token_price_per_m":"9.00000001"}],"complete":true}`)), Header: make(http.Header)}, nil
	})}
	s, err := NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://upstream.example/v1", APIKey: "secret-token", AllowedModels: map[string]struct{}{"alpha/model": {}}}, client, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	got, fetchErr := s.ObserveCatalog(context.Background())
	if fetchErr != nil {
		t.Fatal(fetchErr)
	}
	if !got.Successful || !got.Complete || !got.Fresh || !got.FetchedAt.Equal(now) || len(got.Models) != 1 {
		t.Fatalf("observation=%#v", got)
	}
	model := got.Models[0]
	if model.NormalizedID != "alpha/model" || model.InputPriceUSDPerMillionTokens == nil || *model.InputPriceUSDPerMillionTokens != "0.12345678" || model.OutputPriceUSDPerMillionTokens == nil || *model.OutputPriceUSDPerMillionTokens != "9.00000001" {
		t.Fatalf("model=%#v", model)
	}
	if model.Provider != "Provider" {
		t.Fatalf("provider=%q", model.Provider)
	}
}

func TestCatalogObservationOmittedCompletenessFailsClosed(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})}
	s, err := NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://upstream.example/v1", APIKey: "secret", AllowedModels: map[string]struct{}{"alpha/model": {}}}, client, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ObserveCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Successful || got.Complete || !got.Fresh {
		t.Fatalf("observation=%#v", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
