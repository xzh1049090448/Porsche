package whitelabel

import (
	"bytes"
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
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"alpha/model","owned_by":"Provider","input_token_price_per_m":0.12345678,"output_token_price_per_m":"9.00000001"}],"complete":true}`)), Header: make(http.Header)}, nil
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
	if err == nil || got.Successful || got.Complete || got.Fresh {
		t.Fatalf("observation=%#v", got)
	}
}

func TestCatalogObservationExplicitIncompleteAndEmptyCatalogs(t *testing.T) {
	incomplete, err := catalogObservationService(t, `{"data":[],"complete":false}`).ObserveCatalog(context.Background())
	if err != nil || !incomplete.Successful || incomplete.Complete || !incomplete.Fresh || len(incomplete.Models) != 0 {
		t.Fatalf("explicit incomplete observation=%#v err=%v", incomplete, err)
	}
	empty, err := catalogObservationService(t, `{"data":[],"complete":true}`).ObserveCatalog(context.Background())
	if err != nil || !empty.Successful || !empty.Complete || !empty.Fresh || len(empty.Models) != 0 {
		t.Fatalf("explicit complete empty observation=%#v err=%v", empty, err)
	}
}

func TestCatalogObservationStrictBoundedDocument(t *testing.T) {
	valid := `{"data":[],"complete":true}`
	exactLimit := valid + strings.Repeat(" ", catalogObservationMaxBytes-len(valid))
	tests := []struct {
		name       string
		body       string
		wantOK     bool
		wantClosed bool
	}{
		{name: "exact limit", body: exactLimit, wantOK: true},
		{name: "max plus one valid prefix", body: exactLimit + " ", wantClosed: true},
		{name: "trailing whitespace", body: valid + " \n\t", wantOK: true},
		{name: "trailing object", body: valid + `{}`, wantClosed: true},
		{name: "trailing garbage", body: valid + `x`, wantClosed: true},
		{name: "truncated json", body: `{"data":[`, wantClosed: true},
		{name: "truncated utf8", body: string(append([]byte(`{"data":[],"complete":true,"x":"`), 0xe2, 0x82)), wantClosed: true},
		{name: "duplicate complete false true", body: `{"data":[],"complete":false,"complete":true}`, wantClosed: true},
		{name: "duplicate complete true false", body: `{"data":[],"complete":true,"complete":false}`, wantClosed: true},
		{name: "unknown root field", body: `{"data":[],"complete":true,"unexpected":1}`, wantClosed: true},
		{name: "unknown model field", body: `{"data":[{"id":"alpha/model","owned_by":"p","unexpected":1}],"complete":true}`, wantClosed: true},
		{name: "malformed complete type", body: `{"data":[],"complete":"true"}`, wantClosed: true},
		{name: "malformed data type", body: `{"data":{},"complete":true}`, wantClosed: true},
		{name: "malformed price type", body: `{"data":[{"id":"alpha/model","owned_by":"p","input_token_price_per_m":{}}],"complete":true}`, wantClosed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := catalogObservationService(t, test.body)
			got, err := s.ObserveCatalog(context.Background())
			if test.wantOK {
				if err != nil || !got.Successful || !got.Complete || !got.Fresh {
					t.Fatalf("observation=%#v err=%v", got, err)
				}
				return
			}
			if !test.wantClosed || err == nil || got.Successful || got.Complete || got.Fresh {
				t.Fatalf("fail-closed observation=%#v err=%v", got, err)
			}
		})
	}
}

func catalogObservationService(t *testing.T, body string) *WhiteLabelService {
	return catalogObservationBytesService(t, []byte(body))
}

func catalogObservationBytesService(t *testing.T, body []byte) *WhiteLabelService {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	s, err := NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://upstream.example/v1", APIKey: "secret", AllowedModels: map[string]struct{}{"alpha/model": {}}}, client, func() time.Time { return time.Unix(1_900_000_000, 0).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCatalogObservationRequiresExactCaseSensitiveUTF8SchemaKeys(t *testing.T) {
	canonical := `{"data":[{"id":"alpha/model","owned_by":"p","input_token_price_per_m":"1","output_token_price_per_m":"2"}],"complete":true}`
	cases := []struct {
		name string
		body []byte
	}{
		{name: "Complete alone", body: []byte(`{"data":[],"Complete":true}`)},
		{name: "COMPLETE alone", body: []byte(`{"data":[],"COMPLETE":true}`)},
		{name: "canonical then case variant", body: []byte(`{"data":[],"complete":false,"Complete":true}`)},
		{name: "case variant then canonical", body: []byte(`{"data":[],"Complete":true,"complete":false}`)},
		{name: "Data", body: []byte(`{"Data":[],"complete":true}`)},
		{name: "ID", body: []byte(`{"data":[{"ID":"alpha/model"}],"complete":true}`)},
		{name: "Owned_By", body: []byte(`{"data":[{"id":"alpha/model","Owned_By":"p"}],"complete":true}`)},
		{name: "price case variant", body: []byte(`{"data":[{"id":"alpha/model","Input_token_price_per_m":"1"}],"complete":true}`)},
		{name: "nested canonical then variant", body: []byte(`{"data":[{"id":"alpha/model","owned_by":"p","Owned_By":"q"}],"complete":true}`)},
		{name: "nested variant then canonical", body: []byte(`{"data":[{"id":"alpha/model","Owned_By":"q","owned_by":"p"}],"complete":true}`)},
		{name: "unicode lookalike complete", body: []byte(`{"data":[],"completе":true}`)},
		{name: "unicode lookalike id", body: []byte(`{"data":[{"іd":"alpha/model"}],"complete":true}`)},
		{name: "invalid utf8 key", body: append(append([]byte(`{"data":[],"`), 0xff), []byte(`":true,"complete":true}`)...)},
		{name: "invalid utf8 value", body: append(append([]byte(`{"data":[{"id":"`), 0xff), []byte(`"}],"complete":true}`)...)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := catalogObservationBytesService(t, test.body).ObserveCatalog(context.Background())
			if err == nil || got.Successful || got.Complete || got.Fresh {
				t.Fatalf("fail-closed observation=%#v err=%v", got, err)
			}
		})
	}
	exact := canonical + strings.Repeat(" ", catalogObservationMaxBytes-len(canonical))
	got, err := catalogObservationService(t, exact).ObserveCatalog(context.Background())
	if err != nil || !got.Successful || !got.Complete || !got.Fresh || len(got.Models) != 1 {
		t.Fatalf("canonical exact-limit observation=%#v err=%v", got, err)
	}
}

func TestCatalogObservationRejectsMissingRequiredSchemaFields(t *testing.T) {
	tests := []string{
		`{"complete":true}`,
		`{"data":[]}`,
		`{"data":[{}],"complete":true}`,
		`{"data":[{"input_token_price_per_m":"1"}],"complete":true}`,
		`{"data":[{"id":null}],"complete":true}`,
		`{"data":[{"id":" "}],"complete":true}`,
		`{"data":[{"id":" alpha/model "}],"complete":true}`,
		`{"data":[{"id":"Alpha Model"}],"complete":true}`,
		`{"data":[{"id":"alpha/model","id":"beta/model"}],"complete":true}`,
		`{"data":[{"id":"alpha/model","ID":"beta/model"}],"complete":true}`,
		`{"data":[{"ID":"beta/model","id":"alpha/model"}],"complete":true}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			got, err := catalogObservationService(t, body).ObserveCatalog(context.Background())
			if err == nil || got.Successful || got.Complete || got.Fresh {
				t.Fatalf("fail-closed observation=%#v err=%v", got, err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
