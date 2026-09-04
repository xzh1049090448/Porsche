package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

type actionExecuteStub struct {
	outcome TerminalOutcome
	err     error
	calls   int
}

func (stub *actionExecuteStub) Execute(_ context.Context, _ *gorm.DB, _ models.AdminOperation) (TerminalOutcome, error) {
	stub.calls++
	return stub.outcome, stub.err
}

func TestActionExecuteTerminalOutcomeContract(t *testing.T) {
	guid := int64(44)
	failure := models.FailureActionRejected
	for _, tc := range []struct {
		name    string
		outcome TerminalOutcome
		wantErr bool
	}{
		{name: "success none", outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}},
		{name: "success user", outcome: TerminalOutcome{ResultKind: models.ResultUser, ResultGUID: &guid, HTTPStatus: 200}},
		{name: "known rejection", outcome: TerminalOutcome{Failure: &failure, HTTPStatus: 409}},
		{name: "success failure status", outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 500}, wantErr: true},
		{name: "failed with result", outcome: TerminalOutcome{Failure: &failure, ResultKind: models.ResultUser, ResultGUID: &guid, HTTPStatus: 409}, wantErr: true},
		{name: "unknown failure", outcome: TerminalOutcome{Failure: func() *models.AdminOperationFailure { value := models.AdminOperationFailure(99); return &value }(), HTTPStatus: 409}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTerminalOutcome(tc.outcome)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateTerminalOutcome() = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestCommitUnknownErrorRedactsCause(t *testing.T) {
	cause := errors.New("private database endpoint and secret")
	err := &CommitUnknownError{PublicRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Cause: cause}
	if err.Error() != "admin operation commit outcome unknown" || !errors.Is(err, cause) {
		t.Fatalf("commit unknown contract = %q unwrap=%v", err.Error(), errors.Is(err, cause))
	}
}

func TestActionExecuteIdentityKeepsClaimsPrivateAndRedacted(t *testing.T) {
	raw := [32]byte{}
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	identity := &OperationIdentity{ID: 1, PublicRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", actor: ActionActor{UserID: 2, UserGUID: 3, AuthVersion: 4, SessionSID: "11111111-2222-4333-8444-555555555555", SessionVersion: 5}, capability: newOperationLeaseCapability(&raw)}
	if !operationLeaseIsZero(&raw) || identity.capability == nil {
		t.Fatal("lease capability constructor did not move and clear raw input")
	}
	if !validOperationActorClaims(identity.actor) {
		t.Fatal("private actor binding lost")
	}
	canonical := `OperationIdentity{PublicRef:"op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	for _, subject := range []any{*identity, identity} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			got := fmt.Sprintf(format, subject)
			want := canonical
			if format == "%q" {
				want = strconv.Quote(canonical)
			}
			if got != want || strings.Contains(got, identity.actor.SessionSID) || strings.Contains(got, "actor") || strings.Contains(got, "capability") || strings.Contains(got, "ID:") || strings.Contains(got, "[1 2") {
				t.Fatalf("fmt.Sprintf(%q) = %q, want safe %q", format, got, want)
			}
		}
	}
	if identity.String() != canonical || identity.GoString() != canonical || (*identity).String() != canonical || (*identity).GoString() != canonical {
		t.Fatalf("identity formatting = %q / %q", identity.String(), identity.GoString())
	}
	expectedJSON := `{"public_ref":"op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	for _, subject := range []any{*identity, identity} {
		encoded, err := json.Marshal(subject)
		if err != nil || string(encoded) != expectedJSON {
			t.Fatalf("identity JSON = %s, %v", encoded, err)
		}
	}
	valueNested, err := json.Marshal(struct {
		Identity OperationIdentity `json:"identity"`
	}{Identity: *identity})
	if err != nil || string(valueNested) != `{"identity":`+expectedJSON+`}` {
		t.Fatalf("nested value JSON = %s, %v", valueNested, err)
	}
	pointerNested, err := json.Marshal(struct {
		Identity *OperationIdentity `json:"identity"`
	}{Identity: identity})
	if err != nil || string(pointerNested) != `{"identity":`+expectedJSON+`}` {
		t.Fatalf("nested pointer JSON = %s, %v", pointerNested, err)
	}
	identity.capability.mu.Lock()
	stillReady := !identity.capability.consumed && !operationLeaseIsZero(&identity.capability.raw)
	identity.capability.mu.Unlock()
	if !stillReady {
		t.Fatal("formatting or JSON consumed the lease capability")
	}
}

func TestActionOperationBeginBindsExactActorClaimsForExecute(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			var operation *models.AdminOperation
			if existing {
				operation = &models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationProcessing}
			}
			service, _, actor, key, ticket := actionOperationFixture(t, 1_800_000_000_000, operation)
			identity, _, err := service.Begin(context.Background(), OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: testNoopIntent(testNoopTargetGUID, "same-intent")})
			if err != nil || identity == nil {
				t.Fatalf("Begin identity = %#v, %v", identity, err)
			}
			if identity.actor != actor {
				t.Fatalf("Begin actor binding = %#v, want exact claims", identity.actor)
			}
			if existing && identity.capability != nil {
				t.Fatal("existing Begin unexpectedly returned an executable capability")
			}
			if !existing && identity.capability == nil {
				t.Fatal("new Begin omitted executable capability")
			}
			if strings.Contains(identity.String()+identity.GoString(), actor.SessionSID) {
				t.Fatal("Begin identity formatting leaked SID")
			}
		})
	}
}
