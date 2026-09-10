package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type publicVerificationEnvelope struct {
	Action          string          `json:"action"`
	Intent          json.RawMessage `json:"intent"`
	CurrentPassword string          `json:"current_password"`
}

type publicModelDeleteVerificationIntent struct {
	TargetGUID       string `json:"target_guid"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}

type publicRevisionVerificationIntent struct {
	ExpectedRevision int64  `json:"expected_revision"`
	ReleaseGUID      string `json:"release_guid,omitempty"`
	PriceReleaseGUID string `json:"price_release_guid,omitempty"`
}

func issuePublicAdminVerification(c *gin.Context, backend userManagementActionBackend, settings *config.Settings, raw []byte) {
	if dto.ValidateNoDuplicateJSON(raw, 64) != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	var envelope publicVerificationEnvelope
	if !decodeExactPublicVerification(raw, &envelope) || envelope.CurrentPassword == "" {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	password := []byte(envelope.CurrentPassword)
	envelope.CurrentPassword = ""
	defer clear(password)
	action, target, intent, ok := decodePublicVerificationIntent(envelope.Action, envelope.Intent)
	if !ok {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	trustedIP := ""
	if settings != nil {
		trustedIP = httpx.ClientIP(c, settings.TrustProxyHeaders, settings.TrustedProxyCIDRs)
	}
	issued, err := backend.Issue(c.Request.Context(), service.VerificationIssue{Action: action, Actor: adminUserActionActor(c), TargetGUID: target, Intent: intent, CurrentPassword: password, TrustedIP: trustedIP})
	if err != nil {
		adminUserActionError(c, err, "")
		return
	}
	if issued == nil || issued.ExpiresAt <= 0 {
		adminUserActionError(c, service.ErrActionVerificationUnavailable, "")
		return
	}
	if _, err := actionsecurity.ParseTicket([]string{issued.Ticket}); err != nil {
		adminUserActionError(c, service.ErrActionVerificationUnavailable, "")
		return
	}
	c.JSON(http.StatusCreated, dto.UserDeleteIssueResponse{Ticket: issued.Ticket, ExpiresAt: issued.ExpiresAt})
}

func decodeExactPublicVerification(raw []byte, out *publicVerificationEnvelope) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		return false
	}
	var trailing any
	return dec.Decode(&trailing) == io.EOF && out.Action != "" && len(out.Intent) != 0
}

func decodePublicVerificationIntent(name string, raw []byte) (actionsecurity.Action, *int64, any, bool) {
	decode := func(out any) bool {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(out) != nil {
			return false
		}
		var trailing any
		return dec.Decode(&trailing) == io.EOF
	}
	switch name {
	case "public_models.delete":
		var value publicModelDeleteVerificationIntent
		if !decode(&value) || value.ExpectedRevision < 1 || value.Reason == "" || value.Reason != strings.TrimSpace(value.Reason) {
			return 0, nil, nil, false
		}
		guid, ok := publicAdminGUID(value.TargetGUID)
		if !ok {
			return 0, nil, nil, false
		}
		return actionsecurity.ActionPublicModelDelete, &guid, actionsecurity.PublicModelDeleteIntent{ModelGUID: guid, ExpectedRevision: value.ExpectedRevision, Reason: value.Reason}, true
	case "public_pricing.publish":
		var value struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if !decode(&value) || value.ExpectedRevision < 1 {
			return 0, nil, nil, false
		}
		return actionsecurity.ActionPublicPricingPublish, nil, actionsecurity.PublicPricingPublishIntent{ExpectedRevision: value.ExpectedRevision}, true
	case "public_pricing.restore", "public_content.restore":
		var value struct {
			ReleaseGUID      string `json:"release_guid"`
			ExpectedRevision int64  `json:"expected_revision"`
		}
		if !decode(&value) || value.ExpectedRevision < 1 {
			return 0, nil, nil, false
		}
		guid, ok := publicAdminGUID(value.ReleaseGUID)
		if !ok {
			return 0, nil, nil, false
		}
		if name == "public_pricing.restore" {
			return actionsecurity.ActionPublicPricingRestore, &guid, actionsecurity.PublicPricingRestoreIntent{ReleaseGUID: guid, ExpectedRevision: value.ExpectedRevision}, true
		}
		return actionsecurity.ActionPublicContentRestore, &guid, actionsecurity.PublicContentRestoreIntent{ReleaseGUID: guid, ExpectedRevision: value.ExpectedRevision}, true
	case "public_content.publish":
		var value struct {
			PriceReleaseGUID string `json:"price_release_guid"`
			ExpectedRevision int64  `json:"expected_revision"`
		}
		if !decode(&value) || value.ExpectedRevision < 1 {
			return 0, nil, nil, false
		}
		guid, ok := publicAdminGUID(value.PriceReleaseGUID)
		if !ok {
			return 0, nil, nil, false
		}
		return actionsecurity.ActionPublicContentPublish, &guid, actionsecurity.PublicContentPublishIntent{PriceReleaseGUID: guid, ExpectedRevision: value.ExpectedRevision}, true
	default:
		return 0, nil, nil, false
	}
}
