package session

import (
	"errors"
	"testing"
)

func TestAppError_ErrorDoesNotExposeCause(t *testing.T) {
	cause := errors.New("authorization header contains a secret")
	err := &AppError{
		Code:        ErrProviderUnavailable,
		Operation:   "provider.validate",
		UserMessage: "Provider validation failed.",
		Cause:       cause,
		Retryable:   true,
	}

	if got := err.Error(); got != "Provider validation failed." {
		t.Fatalf("got %q, want safe user message", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause is not available through errors.Is")
	}
}

func TestAppError_ErrorFallsBackToOperationAndCode(t *testing.T) {
	err := &AppError{
		Code:      ErrInvalidState,
		Operation: "session.start_turn",
	}

	if got := err.Error(); got != "session.start_turn: invalid-state" {
		t.Fatalf("got %q, want operation and code", got)
	}
}
