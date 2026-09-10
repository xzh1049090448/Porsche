package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/porsche/ai-gateway-go/internal/service"
)

const PublicModelRequestBodyLimit = 1 << 20

var ErrPublicModelRequestTooLarge = errors.New("public model request body too large")

type OptionalNullableString = service.OptionalNullableString
type PublicModelAdmin = service.PublicModelAdmin
type CreatePublicModelRequest = service.CreatePublicModelRequest
type UpdatePublicModelRequest = service.UpdatePublicModelRequest
type RevisionRequest = service.RevisionRequest
type DeactivationRequest = service.DeactivationRequest
type DeletePublicModelRequest = service.DeletePublicModelRequest
type AdminModelListRequest = service.AdminModelListRequest
type AdminModelListResponse = service.AdminModelListResponse

func DecodeUpdatePublicModelRequest(r io.Reader) (UpdatePublicModelRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(r, PublicModelRequestBodyLimit+1))
	if err != nil {
		return UpdatePublicModelRequest{}, err
	}
	if len(raw) > PublicModelRequestBodyLimit {
		return UpdatePublicModelRequest{}, ErrPublicModelRequestTooLarge
	}
	if err := ValidateNoDuplicateJSON(raw, 64); err != nil {
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
