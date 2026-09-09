package dto

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
	ExpectedRevision               int64     `json:"expected_revision"`
	DisplayName                    *string   `json:"display_name,omitempty"`
	Provider                       *string   `json:"provider,omitempty"`
	Capabilities                   *[]string `json:"capabilities,omitempty"`
	ContextWindow                  *int64    `json:"context_window,omitempty"`
	InputPriceUSDPerMillionTokens  *string   `json:"input_price_usd_per_million_tokens,omitempty"`
	OutputPriceUSDPerMillionTokens *string   `json:"output_price_usd_per_million_tokens,omitempty"`
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
