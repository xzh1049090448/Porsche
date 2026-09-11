package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func TestDecodePublicVerificationIntentMatchesExecutionBindings(t *testing.T) {
	modelGUID, priceGUID, releaseGUID := int64(101), int64(202), int64(303)
	for _, tc := range []struct {
		name   string
		raw    string
		action actionsecurity.Action
		target *int64
		intent any
	}{
		{"public_models.delete", `{"target_guid":"101","expected_revision":4,"reason":"retired"}`, actionsecurity.ActionPublicModelDelete, &modelGUID, actionsecurity.PublicModelDeleteIntent{ModelGUID: 101, ExpectedRevision: 4, Reason: "retired"}},
		{"public_pricing.publish", `{"expected_revision":5}`, actionsecurity.ActionPublicPricingPublish, nil, actionsecurity.PublicPricingPublishIntent{ExpectedRevision: 5}},
		{"public_pricing.restore", `{"release_guid":"303","expected_revision":6}`, actionsecurity.ActionPublicPricingRestore, &releaseGUID, actionsecurity.PublicPricingRestoreIntent{ReleaseGUID: 303, ExpectedRevision: 6}},
		{"public_content.publish", `{"price_release_guid":"202","expected_revision":7}`, actionsecurity.ActionPublicContentPublish, &priceGUID, actionsecurity.PublicContentPublishIntent{PriceReleaseGUID: 202, ExpectedRevision: 7}},
		{"public_content.restore", `{"release_guid":"303","expected_revision":8}`, actionsecurity.ActionPublicContentRestore, &releaseGUID, actionsecurity.PublicContentRestoreIntent{ReleaseGUID: 303, ExpectedRevision: 8}},
	} {
		action, target, intent, ok := decodePublicVerificationIntent(tc.name, []byte(tc.raw))
		if !ok || action != tc.action || !reflect.DeepEqual(target, tc.target) || !reflect.DeepEqual(intent, tc.intent) {
			t.Fatalf("%s decoded action=%v target=%v intent=%#v ok=%v", tc.name, action, target, intent, ok)
		}
	}
}

func TestPublicVerificationHTTPDispatchesFiveReachableActions(t *testing.T) {
	for _, tc := range []struct {
		body   string
		action actionsecurity.Action
		intent any
	}{
		{`{"action":"public_models.delete","intent":{"target_guid":"101","expected_revision":4,"reason":"retired"},"current_password":"Current!Pass9"}`, actionsecurity.ActionPublicModelDelete, actionsecurity.PublicModelDeleteIntent{ModelGUID: 101, ExpectedRevision: 4, Reason: "retired"}},
		{`{"action":"public_pricing.publish","intent":{"expected_revision":5},"current_password":"Current!Pass9"}`, actionsecurity.ActionPublicPricingPublish, actionsecurity.PublicPricingPublishIntent{ExpectedRevision: 5}},
		{`{"action":"public_pricing.restore","intent":{"release_guid":"303","expected_revision":6},"current_password":"Current!Pass9"}`, actionsecurity.ActionPublicPricingRestore, actionsecurity.PublicPricingRestoreIntent{ReleaseGUID: 303, ExpectedRevision: 6}},
		{`{"action":"public_content.publish","intent":{"price_release_guid":"202","expected_revision":7},"current_password":"Current!Pass9"}`, actionsecurity.ActionPublicContentPublish, actionsecurity.PublicContentPublishIntent{PriceReleaseGUID: 202, ExpectedRevision: 7}},
		{`{"action":"public_content.restore","intent":{"release_guid":"303","expected_revision":8},"current_password":"Current!Pass9"}`, actionsecurity.ActionPublicContentRestore, actionsecurity.PublicContentRestoreIntent{ReleaseGUID: 303, ExpectedRevision: 8}},
	} {
		backend := adminUserCreateBackend("admin")
		backend.issued = &service.IssuedVerification{Ticket: testActionTicket, ExpiresAt: 1790000300000}
		engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
		response := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", tc.body, nil)
		if response.Code != http.StatusCreated || backend.issueCalls != 1 || backend.issueAction != tc.action || !reflect.DeepEqual(backend.issueAny, tc.intent) {
			t.Fatalf("action=%v status=%d calls=%d gotAction=%v intent=%#v body=%s", tc.action, response.Code, backend.issueCalls, backend.issueAction, backend.issueAny, response.Body.String())
		}
		for _, value := range backend.issueCurrent {
			if value != 0 {
				t.Fatal("public verification retained current_password bytes")
			}
		}
		assertActionTestExactBody(t, response, `{"ticket":"`+testActionTicket+`","expires_at":1790000300000}`)
	}
}

func TestPublicVerificationPasswordWireNeverUsesStringAndClearsOnServiceError(t *testing.T) {
	field, ok := reflect.TypeOf(publicVerificationEnvelope{}).FieldByName("CurrentPassword")
	if !ok || field.Type != reflect.TypeOf(json.RawMessage{}) || field.Type.Kind() == reflect.String {
		t.Fatalf("current password wire type=%v", field.Type)
	}
	backend := adminUserCreateBackend("admin")
	backend.issueErr = errors.New("injected service failure")
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
	response := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", `{"action":"public_pricing.publish","intent":{"expected_revision":5},"current_password":"Current!Pass9"}`, nil)
	if response.Code != http.StatusServiceUnavailable || backend.issueCalls != 1 {
		t.Fatalf("service failure status=%d calls=%d", response.Code, backend.issueCalls)
	}
	for _, value := range backend.issueCurrent {
		if value != 0 {
			t.Fatal("service error retained current_password bytes")
		}
	}
}

func TestPublicVerificationEnvelopeClearsOwnedRawMessageBuffers(t *testing.T) {
	raw := []byte(`{"action":"public_pricing.publish","intent":{"expected_revision":5},"current_password":"Current!Pass9"}`)
	var envelope publicVerificationEnvelope
	if !decodeExactPublicVerification(raw, &envelope) {
		t.Fatal("decode valid envelope")
	}
	passwordBacking := envelope.CurrentPassword
	intentBacking := envelope.Intent
	envelope.clearRawMessages()
	if envelope.CurrentPassword != nil || envelope.Intent != nil {
		t.Fatal("clear left raw message references attached")
	}
	for _, owned := range [][]byte{passwordBacking, intentBacking} {
		for _, value := range owned {
			if value != 0 {
				t.Fatal("clear left owned raw message bytes")
			}
		}
	}

	for _, malformed := range [][]byte{
		[]byte(`{"action":"public_pricing.publish","intent":{"expected_revision":5},"current_password":"unterminated}`),
		[]byte(`{"action":"public_pricing.publish","intent":{"expected_revision":5},"current_password":{"partial":"secret"},"unknown":1}`),
	} {
		var invalid publicVerificationEnvelope
		if decodeExactPublicVerification(malformed, &invalid) {
			t.Fatal("accepted malformed password envelope")
		}
		if invalid.CurrentPassword != nil || invalid.Intent != nil {
			t.Fatal("decode error retained raw message buffers")
		}
	}
}

func TestPublicVerificationRejectsUnknownDuplicateEscapedAndDeepJSON(t *testing.T) {
	for _, raw := range []string{`{"expected_revision":1,"expected_revision":2}`, `{"payload":{"a":1,"\u0061":2}}`} {
		if dto.ValidateNoDuplicateJSON([]byte(raw), 64) == nil {
			t.Fatalf("accepted duplicate verification JSON %s", raw)
		}
	}
	for _, raw := range []string{`{"expected_revision":1,"unknown":2}`, `{"expected_revision":1} {}`} {
		if _, _, _, ok := decodePublicVerificationIntent("public_pricing.publish", []byte(raw)); ok {
			t.Fatalf("accepted invalid verification intent %s", raw)
		}
	}
}
