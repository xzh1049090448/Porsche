package service

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// VerifyAndConsumeInTx verifies and consumes an action ticket using the
// business transaction supplied by the public administration service.
type VerifyAndConsumeInTx func(context.Context, *gorm.DB) error

type publicAdminTransactionOptions struct {
	consume VerifyAndConsumeInTx
}

// PublicAdminTransactionOption adds a transaction participant to a destructive
// public administration mutation.
type PublicAdminTransactionOption func(*publicAdminTransactionOptions) error

// WithActionTicketConsume joins action-ticket consumption to the business
// transaction. A successful idempotent replay does not invoke the callback.
func WithActionTicketConsume(consume VerifyAndConsumeInTx) PublicAdminTransactionOption {
	return func(options *publicAdminTransactionOptions) error {
		if consume == nil || options.consume != nil {
			return errors.New("invalid action ticket consumer")
		}
		options.consume = consume
		return nil
	}
}

func resolvePublicAdminTransactionOptions(values []PublicAdminTransactionOption) (publicAdminTransactionOptions, error) {
	var options publicAdminTransactionOptions
	for _, value := range values {
		if value == nil {
			return options, errors.New("invalid public admin transaction option")
		}
		if err := value(&options); err != nil {
			return options, err
		}
	}
	return options, nil
}

func (options publicAdminTransactionOptions) consumeTicket(ctx context.Context, tx *gorm.DB) error {
	if options.consume == nil {
		return nil
	}
	return options.consume(ctx, tx)
}
