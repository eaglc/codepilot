package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestHTTPStatusErrorCodes(t *testing.T) {
	tests := []struct {
		status    int
		code      ErrorCode
		retryable bool
	}{
		{status: 401, code: ErrorAuthenticationFailed},
		{status: 403, code: ErrorAuthenticationFailed},
		{status: 408, code: ErrorTimeout, retryable: true},
		{status: 429, code: ErrorRateLimited, retryable: true},
		{status: 503, code: ErrorConnectionFailed, retryable: true},
	}
	for _, test := range tests {
		err := HTTPStatusError("test", test.status)
		code, message, retryable, ok := ErrorInfo(err)
		if !ok || code != test.code || retryable != test.retryable || message == "" {
			t.Fatalf("status %d: code=%q message=%q retryable=%v ok=%v", test.status, code, message, retryable, ok)
		}
	}
}

func TestClassifyTransportErrorIsSafeAndStable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code ErrorCode
	}{
		{name: "timeout", err: context.DeadlineExceeded, code: ErrorTimeout},
		{name: "authentication", err: errors.New("status code: 401 secret-value"), code: ErrorAuthenticationFailed},
		{name: "rate limit", err: errors.New("too many requests secret-value"), code: ErrorRateLimited},
		{name: "model", err: errors.New("model not found secret-value"), code: ErrorModelNotFound},
		{name: "connection", err: errors.New("dial tcp connection refused secret-value"), code: ErrorConnectionFailed},
		{name: "stream", err: io.ErrUnexpectedEOF, code: ErrorStreamInterrupted},
		{name: "unknown", err: errors.New("sdk failed secret-value"), code: ErrorProviderFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified := ClassifyTransportError("test", test.err)
			code, message, _, ok := ErrorInfo(classified)
			if !ok || code != test.code {
				t.Fatalf("classification code=%q ok=%v err=%v", code, ok, classified)
			}
			if strings.Contains(message, "secret-value") {
				t.Fatalf("cause leaked through safe message: %q", message)
			}
		})
	}
}
