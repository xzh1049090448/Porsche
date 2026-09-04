package service

import (
	"context"
	"errors"
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
