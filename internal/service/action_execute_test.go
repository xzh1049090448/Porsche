package service

import (
	"context"
	"encoding/json"
	"errors"
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
	identity := &OperationIdentity{ID: 1, PublicRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", actor: ActionActor{UserID: 2, UserGUID: 3, AuthVersion: 4, SessionSID: "11111111-2222-4333-8444-555555555555", SessionVersion: 5}}
	for i := range identity.LeaseOwner {
		identity.LeaseOwner[i] = byte(i + 1)
	}
	if !validOperationActorClaims(identity.actor) {
		t.Fatal("private actor binding lost")
	}
	formatted := identity.String() + identity.GoString()
	if strings.Contains(formatted, identity.actor.SessionSID) || strings.Contains(formatted, "LeaseOwner") || strings.Contains(formatted, "actor") || strings.Contains(formatted, "ID:") {
		t.Fatalf("identity formatting leaked private claims: %s", formatted)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	expectedJSON := `{"public_ref":"op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	if string(encoded) != expectedJSON || strings.Contains(string(encoded), identity.actor.SessionSID) || strings.Contains(string(encoded), "actor") || strings.Contains(string(encoded), "LeaseOwner") || strings.Contains(string(encoded), "ID") || strings.Contains(string(encoded), "[") {
		t.Fatalf("identity JSON is not exact public_ref only: %s", encoded)
	}
	expectedString := `OperationIdentity{PublicRef:"op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	if identity.String() != expectedString || identity.GoString() != expectedString {
		t.Fatalf("identity formatting = %q / %q", identity.String(), identity.GoString())
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
			identity, _, err := service.Begin(context.Background(), OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: "same-intent"})
			if err != nil || identity == nil {
				t.Fatalf("Begin identity = %#v, %v", identity, err)
			}
			if identity.actor != actor {
				t.Fatalf("Begin actor binding = %#v, want exact claims", identity.actor)
			}
			if strings.Contains(identity.String()+identity.GoString(), actor.SessionSID) {
				t.Fatal("Begin identity formatting leaked SID")
			}
		})
	}
}
