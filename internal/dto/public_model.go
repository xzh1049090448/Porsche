package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type OptionalNullableString struct {
	Set   bool
	Value *string
}

func (o *OptionalNullableString) UnmarshalJSON(raw []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		o.Value = nil
		return nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}
func (o OptionalNullableString) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*o.Value)
}

type PublicModelAdmin struct {
	GUID                           string   `json:"guid"`
	ModelKey                       string   `json:"model_key"`
	UpstreamModelID                string   `json:"upstream_model_id"`
	DisplayName                    string   `json:"display_name"`
	Provider                       string   `json:"provider"`
	Capabilities                   []string `json:"capabilities"`
	ContextWindow                  int64    `json:"context_window"`
	InputPriceUSDPerMillionTokens  *string  `json:"input_price_usd_per_million_tokens"`
	OutputPriceUSDPerMillionTokens *string  `json:"output_price_usd_per_million_tokens"`
	Status                         string   `json:"status"`
	Revision                       int64    `json:"revision"`
	LastUpstreamCheckAt            *int64   `json:"last_upstream_check_at"`
}

type CreatePublicModelRequest struct {
	UpstreamModelID                string   `json:"upstream_model_id"`
	ModelKey                       string   `json:"model_key"`
	DisplayName                    string   `json:"display_name"`
	Provider                       string   `json:"provider"`
	Capabilities                   []string `json:"capabilities"`
	ContextWindow                  int64    `json:"context_window"`
	InputPriceUSDPerMillionTokens  *string  `json:"input_price_usd_per_million_tokens"`
	OutputPriceUSDPerMillionTokens *string  `json:"output_price_usd_per_million_tokens"`
}

type UpdatePublicModelRequest struct {
	ExpectedRevision               int64                  `json:"expected_revision"`
	DisplayName                    *string                `json:"display_name,omitempty"`
	Provider                       *string                `json:"provider,omitempty"`
	Capabilities                   *[]string              `json:"capabilities,omitempty"`
	ContextWindow                  *int64                 `json:"context_window,omitempty"`
	InputPriceUSDPerMillionTokens  OptionalNullableString `json:"input_price_usd_per_million_tokens,omitempty"`
	OutputPriceUSDPerMillionTokens OptionalNullableString `json:"output_price_usd_per_million_tokens,omitempty"`
}

func (r UpdatePublicModelRequest) MarshalJSON() ([]byte, error) {
	m := map[string]any{"expected_revision": r.ExpectedRevision}
	if r.DisplayName != nil {
		m["display_name"] = *r.DisplayName
	}
	if r.Provider != nil {
		m["provider"] = *r.Provider
	}
	if r.Capabilities != nil {
		m["capabilities"] = *r.Capabilities
	}
	if r.ContextWindow != nil {
		m["context_window"] = *r.ContextWindow
	}
	if r.InputPriceUSDPerMillionTokens.Set {
		m["input_price_usd_per_million_tokens"] = r.InputPriceUSDPerMillionTokens.Value
	}
	if r.OutputPriceUSDPerMillionTokens.Set {
		m["output_price_usd_per_million_tokens"] = r.OutputPriceUSDPerMillionTokens.Value
	}
	return json.Marshal(m)
}

type RevisionRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}
type DeactivationRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}
type DeletePublicModelRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}
type AdminModelListRequest struct {
	Search, Status, UpstreamState string
	Page, PageSize                int
}
type AdminModelListResponse struct {
	Items    []PublicModelAdmin `json:"items"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
	Total    int64              `json:"total"`
}

func DecodeUpdatePublicModelRequest(r io.Reader) (UpdatePublicModelRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return UpdatePublicModelRequest{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return UpdatePublicModelRequest{}, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return UpdatePublicModelRequest{}, fmt.Errorf("request must be one object")
	}
	seen := map[string]bool{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return UpdatePublicModelRequest{}, err
		}
		key, ok := keyTok.(string)
		if !ok || seen[key] {
			return UpdatePublicModelRequest{}, fmt.Errorf("duplicate field")
		}
		seen[key] = true
		var discard json.RawMessage
		if err = dec.Decode(&discard); err != nil {
			return UpdatePublicModelRequest{}, err
		}
	}
	if _, err = dec.Token(); err != nil {
		return UpdatePublicModelRequest{}, err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return UpdatePublicModelRequest{}, fmt.Errorf("trailing JSON")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	var out UpdatePublicModelRequest
	if err = strict.Decode(&out); err != nil {
		return out, err
	}
	if out.ExpectedRevision < 1 {
		return out, fmt.Errorf("expected_revision is required")
	}
	return out, nil
}
