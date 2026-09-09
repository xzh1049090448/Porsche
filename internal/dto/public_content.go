package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/porsche/ai-gateway-go/internal/service"
)

const PublicContentRequestBodyLimit = 1 << 20

var ErrPublicContentRequestTooLarge = errors.New("public content request body too large")

type ContentDraft = service.PublicContentDraft
type ContentDraftSaveRequest = service.PublicContentDraftSaveRequest
type ContentPublicationRequest = service.PublicContentPublicationRequest
type ContentRestoreRequest = service.PublicContentRestoreRequest
type PreviewResponse = service.PublicContentPreview
type ImmutableContentReleaseResponse = service.PublicContentReleaseView

func DecodeContentDraftSaveRequest(r io.Reader) (ContentDraftSaveRequest, error) {
	var out ContentDraftSaveRequest
	if err := decodePublicContentObject(r, &out, []string{"expected_revision", "home", "about", "terms", "privacy", "legal_reviewed"}); err != nil {
		return out, err
	}
	if out.ExpectedRevision < 1 || out.Home == nil || out.About == nil || out.Terms == nil || out.Privacy == nil || out.LegalReviewed == nil {
		return out, fmt.Errorf("all content draft fields are required and non-null")
	}
	return out, nil
}

func DecodeContentPublicationRequest(r io.Reader) (ContentPublicationRequest, error) {
	var out ContentPublicationRequest
	if err := decodePublicContentObject(r, &out, []string{"expected_revision", "price_release_guid"}); err != nil {
		return out, err
	}
	if out.ExpectedRevision < 1 || out.PriceReleaseGUID == "" {
		return out, fmt.Errorf("expected_revision and price_release_guid are required")
	}
	return out, nil
}

func DecodeContentRestoreRequest(r io.Reader) (ContentRestoreRequest, error) {
	var out ContentRestoreRequest
	if err := decodePublicContentObject(r, &out, []string{"expected_revision"}); err != nil {
		return out, err
	}
	if out.ExpectedRevision < 1 {
		return out, fmt.Errorf("expected_revision is required")
	}
	return out, nil
}

func decodePublicContentObject(r io.Reader, out any, required []string) error {
	raw, err := io.ReadAll(io.LimitReader(r, PublicContentRequestBodyLimit+1))
	if err != nil {
		return err
	}
	if len(raw) > PublicContentRequestBodyLimit {
		return ErrPublicContentRequestTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("request must be one object")
	}
	seen := map[string]bool{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := k.(string)
		if !ok || seen[key] {
			return fmt.Errorf("duplicate field")
		}
		seen[key] = true
		var v json.RawMessage
		if err = dec.Decode(&v); err != nil {
			return err
		}
	}
	if _, err = dec.Token(); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	for _, key := range required {
		if !seen[key] {
			return fmt.Errorf("missing field %s", key)
		}
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	return strict.Decode(out)
}

func MarshalPublicContentDraft(v ContentDraft) []byte { b, _ := json.Marshal(v); return b }
