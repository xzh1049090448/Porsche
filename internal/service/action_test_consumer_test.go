package service

import (
	"context"
	"errors"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

type fixtureActionConsumer struct {
	outcome TerminalOutcome
	err     error
	calls   int
}

func (consumer *fixtureActionConsumer) Execute(ctx context.Context, tx *gorm.DB, operation models.AdminOperation) (TerminalOutcome, error) {
	consumer.calls++
	if err := tx.WithContext(ctx).Exec("INSERT INTO fixture_action_effects (operation_ref) VALUES (?)", operation.PublicRef).Error; err != nil {
		return TerminalOutcome{}, err
	}
	if err := tx.WithContext(ctx).Exec("INSERT INTO fixture_action_callback_audits (operation_ref) VALUES (?)", operation.PublicRef).Error; err != nil {
		return TerminalOutcome{}, err
	}
	if err := tx.WithContext(ctx).Exec("INSERT INTO fixture_action_callback_outbox (operation_ref) VALUES (?)", operation.PublicRef).Error; err != nil {
		return TerminalOutcome{}, err
	}
	if consumer.err != nil {
		return TerminalOutcome{}, consumer.err
	}
	return consumer.outcome, nil
}

type fixtureActionAuditWriter struct{ calls int }

func (writer *fixtureActionAuditWriter) Write(ctx context.Context, tx *gorm.DB, event ActionAuditEvent) error {
	writer.calls++
	if event.PublicRef == "" || event.ActorGUID <= 0 || event.SessionGUID <= 0 || event.OccurredAt <= 0 {
		return errors.New("invalid redacted audit event")
	}
	return tx.WithContext(ctx).Exec("INSERT INTO fixture_action_official_audits (operation_ref, state) VALUES (?, ?)", event.PublicRef, event.State).Error
}

type fixtureActionOutboxWriter struct{ calls int }

func (writer *fixtureActionOutboxWriter) Write(ctx context.Context, tx *gorm.DB, event ActionOutboxEvent) error {
	writer.calls++
	if event.PublicRef == "" || event.ActorGUID <= 0 || event.SessionGUID <= 0 || event.OccurredAt <= 0 {
		return errors.New("invalid redacted outbox event")
	}
	return tx.WithContext(ctx).Exec("INSERT INTO fixture_action_official_outbox (operation_ref, state) VALUES (?, ?)", event.PublicRef, event.State).Error
}
