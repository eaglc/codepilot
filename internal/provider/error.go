package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// ErrorCode is a stable product classification independent of Provider SDKs.
type ErrorCode string

const (
	ErrorNotConfigured        ErrorCode = "not_configured"
	ErrorCredentialMissing    ErrorCode = "credential_missing"
	ErrorConnectionFailed     ErrorCode = "connection_failed"
	ErrorAuthenticationFailed ErrorCode = "authentication_failed"
	ErrorModelNotFound        ErrorCode = "model_not_found"
	ErrorRateLimited          ErrorCode = "rate_limited"
	ErrorTimeout              ErrorCode = "timeout"
)

var statusPattern = regexp.MustCompile(`(?i)(?:status(?:[\s_]+code)?[=:\s]+|http[/\s][0-9.]*\s+)([1-5][0-9]{2})`)

// ProductError is safe to persist and display. Cause remains available for
// diagnostics through errors.Unwrap, but Error never includes cause text.
type ProductError struct {
	Code      ErrorCode
	Operation string
	Message   string
	Retryable bool
	Cause     error
}

func (e *ProductError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "Provider operation failed."
	}
	return e.Message
}

func (e *ProductError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Temporary exposes retryability through a provider-neutral conventional
// interface consumed by Agent without importing this package.
func (e *ProductError) Temporary() bool { return e != nil && e.Retryable }

// RetryReason returns a stable code without Provider response details.
func (e *ProductError) RetryReason() string {
	if e == nil {
		return "provider_error"
	}
	return string(e.Code)
}

// NewProductError creates a stable, user-safe Provider error.
func NewProductError(code ErrorCode, operation, message string, retryable bool, cause error) error {
	return &ProductError{Code: code, Operation: strings.TrimSpace(operation), Message: strings.TrimSpace(message), Retryable: retryable, Cause: cause}
}

// ErrorInfo extracts stable product details for CLI, TUI and recovery policy.
func ErrorInfo(err error) (ErrorCode, string, bool, bool) {
	var target *ProductError
	if !errors.As(err, &target) {
		return "", "", false, false
	}
	return target.Code, target.Error(), target.Retryable, true
}

// HTTPStatusError converts an endpoint status without retaining response
// bodies, headers or Provider SDK types.
func HTTPStatusError(operation string, status int) error {
	switch status {
	case 401, 403:
		return NewProductError(ErrorAuthenticationFailed, operation, "Provider authentication failed. Check or replace the API key.", false, nil)
	case 408, 504:
		return NewProductError(ErrorTimeout, operation, "Provider request timed out. Check the endpoint and try again.", true, nil)
	case 429:
		return NewProductError(ErrorRateLimited, operation, "Provider rate limit was reached. Wait and try again.", true, nil)
	default:
		if status >= 500 {
			return NewProductError(ErrorConnectionFailed, operation, "Provider service is temporarily unavailable. Try again later.", true, nil)
		}
		return NewProductError(ErrorConnectionFailed, operation, fmt.Sprintf("Provider endpoint returned HTTP %d. Check the configured Base URL.", status), false, nil)
	}
}

// ClassifyTransportError maps network and SDK failures to stable product
// errors. The returned Error string never includes the original error text.
func ClassifyTransportError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var productError *ProductError
	if errors.As(err, &productError) {
		return productError
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewProductError(ErrorTimeout, operation, "Provider request timed out. Check the endpoint and try again.", true, err)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return NewProductError(ErrorTimeout, operation, "Provider request timed out. Check the endpoint and try again.", true, err)
	}
	if status := statusCode(err); status != 0 {
		classified := HTTPStatusError(operation, status)
		if target, ok := classified.(*ProductError); ok {
			target.Cause = err
		}
		return classified
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "invalid api key"), strings.Contains(lower, "authentication"):
		return NewProductError(ErrorAuthenticationFailed, operation, "Provider authentication failed. Check or replace the API key.", false, err)
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "too many requests"):
		return NewProductError(ErrorRateLimited, operation, "Provider rate limit was reached. Wait and try again.", true, err)
	case strings.Contains(lower, "model") && strings.Contains(lower, "not found"):
		return NewProductError(ErrorModelNotFound, operation, "The selected model is unavailable. Choose an installed or accessible model.", false, err)
	default:
		return NewProductError(ErrorConnectionFailed, operation, "Cannot connect to the Provider endpoint. Check the Base URL, network, and local service status.", true, err)
	}
}

func statusCode(err error) int {
	type statusCoder interface{ StatusCode() int }
	var typed statusCoder
	if errors.As(err, &typed) {
		return typed.StatusCode()
	}
	match := statusPattern.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return 0
	}
	status, _ := strconv.Atoi(match[1])
	return status
}
