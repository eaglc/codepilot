package session

import "fmt"

// ErrorCode is a stable application error category exposed to the UI.
type ErrorCode string

const (
	// ErrInvalidInput indicates malformed or unsupported user input.
	ErrInvalidInput ErrorCode = "invalid-input"
	// ErrInvalidState indicates that an operation is not valid in the current state.
	ErrInvalidState ErrorCode = "invalid-state"
	// ErrNotFound indicates that a requested record does not exist.
	ErrNotFound ErrorCode = "not-found"
	// ErrConflict indicates that an operation conflicts with existing state.
	ErrConflict ErrorCode = "conflict"
	// ErrWorkspaceUnavailable indicates that a bound worktree cannot be accessed.
	ErrWorkspaceUnavailable ErrorCode = "workspace-unavailable"
	// ErrProviderUnavailable indicates that a provider or model is unusable.
	ErrProviderUnavailable ErrorCode = "provider-unavailable"
	// ErrPermissionDenied indicates that policy rejected an operation.
	ErrPermissionDenied ErrorCode = "permission-denied"
	// ErrApprovalRequired indicates that an operation is waiting for approval.
	ErrApprovalRequired ErrorCode = "approval-required"
	// ErrCancelled indicates that an operation was cancelled.
	ErrCancelled ErrorCode = "cancelled"
	// ErrTimeout indicates that an operation exceeded its deadline.
	ErrTimeout ErrorCode = "timeout"
	// ErrCorruptedState indicates invalid persisted application state.
	ErrCorruptedState ErrorCode = "corrupted-state"
	// ErrPersistence indicates that durable state could not be read or written.
	ErrPersistence ErrorCode = "persistence"
	// ErrInternal indicates an unexpected internal failure.
	ErrInternal ErrorCode = "internal"
)

// AppError carries a stable code and a safe user-facing message.
type AppError struct {
	Code        ErrorCode
	Operation   string
	UserMessage string
	Cause       error
	Retryable   bool
}

// Error returns a safe description without exposing the wrapped cause.
func (e *AppError) Error() string {
	if e == nil {
		return "<nil>"
	}

	if e.UserMessage != "" {
		return e.UserMessage
	}

	if e.Operation != "" && e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Operation, e.Code)
	}

	if e.Code != "" {
		return string(e.Code)
	}

	return "application error"
}

// Unwrap returns the diagnostic cause for errors.Is and errors.As.
func (e *AppError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Cause
}

// ApprovalRequiredError carries the exact safe request needed by the UI while
// still participating in the stable AppError chain.
type ApprovalRequiredError struct {
	Request ApprovalRequest
}

func (e *ApprovalRequiredError) Error() string {
	return "Approval is required before this action can run."
}

func (e *ApprovalRequiredError) Unwrap() error {
	return &AppError{
		Code:        ErrApprovalRequired,
		Operation:   "workspace.authorize",
		UserMessage: e.Error(),
	}
}
