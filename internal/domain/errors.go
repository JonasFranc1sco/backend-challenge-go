package domain

import (
	"errors"
	"fmt"
)

// Sentinel Domain Errors
var (
	ErrCurrencyMismatch             = errors.New("currency mismatch between monetary values or entities")
	ErrInsufficientBalance          = errors.New("insufficient wallet balance for debit operation")
	ErrReversalInsufficientBalance  = errors.New("insufficient wallet balance to execute reversal debit")
	ErrInvalidAmount                = errors.New("invalid monetary amount format or value")
	ErrInvalidCurrency              = errors.New("invalid currency code: must be 3-letter ISO 4217")
	ErrArithmeticOverflow           = errors.New("arithmetic overflow detected in monetary operation")
	ErrNegativeAmount               = errors.New("monetary amount must be non-negative for this operation")
	ErrZeroAmountNotAllowed         = errors.New("monetary amount must be strictly greater than zero")
	ErrLossAmountMustBeZero         = errors.New("LOSS transaction requires amount to be exactly zero")
	ErrInvalidTransactionKind       = errors.New("unsupported or invalid transaction kind")
	ErrInvalidTransactionStatus     = errors.New("unsupported or invalid transaction status")
	ErrTerminalState                = errors.New("transaction is in a terminal state and cannot be transitioned")
	ErrInvalidStateTransition       = errors.New("illegal transaction state transition requested")
	ErrOpeningNotAllowedExternally  = errors.New("OPENING transactions are strictly internal and forbidden from external sources")
	ErrExternalMetadataMissing      = errors.New("external transactions require provider, external ID, idempotency key, round, and game")
	ErrMissingReference             = errors.New("reference external transaction ID is required for reversals")
	ErrInvalidReference             = errors.New("reference transaction does not match provider, player, currency, round or amount")
	ErrInvalidLedgerMath            = errors.New("ledger entry math does not balance: balanceAfter must equal balanceBefore +/- amount")
	ErrInvalidLedgerDirection       = errors.New("invalid ledger direction: must be DEBIT or CREDIT")
	ErrWalletNotInitialized         = errors.New("wallet aggregate is not properly initialized")
	ErrInvalidWalletVersion         = errors.New("wallet version must be greater than or equal to 1")
)

// Failure codes returned in WagerTransaction
const (
	FailureCodeInsufficientFunds         = "INSUFFICIENT_FUNDS"
	FailureCodeReversalInsufficientFunds = "REVERSAL_INSUFFICIENT_FUNDS"
	FailureCodeReferenceNotFound         = "REFERENCE_NOT_FOUND"
	FailureCodeReferenceMismatch         = "REFERENCE_MISMATCH"
	FailureCodeReferenceFailed           = "REFERENCE_FAILED"
	FailureCodeDuplicateReversal         = "DUPLICATE_REVERSAL"
	FailureCodeInvalidCurrency           = "INVALID_CURRENCY"
	FailureCodeInvalidAmount             = "INVALID_AMOUNT"
	FailureCodeInternalError             = "INTERNAL_ERROR"
)

// DomainError wraps a sentinel error with an optional contextual failure code and detail message.
type DomainError struct {
	Code    string
	Message string
	Err     error
}

func (e *DomainError) Error() string {
	if e.Err != nil {
		if e.Message != "" {
			return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Err)
		}
		return fmt.Sprintf("[%s] %v", e.Code, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *DomainError) Unwrap() error {
	return e.Err
}

func (e *DomainError) Is(target error) bool {
	if target == nil {
		return false
	}
	return errors.Is(e.Err, target)
}

// NewDomainError constructs a new DomainError
func NewDomainError(code string, message string, err error) *DomainError {
	return &DomainError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}
