package llm

import "testing"

func TestResponseErrorNilReceiverIsSafe(t *testing.T) {
	var responseError *ResponseError
	if responseError.Temporary() {
		t.Fatal("nil response error was classified as temporary")
	}
	if got := responseError.RetryReason(); got != "model_response_error" {
		t.Fatalf("RetryReason() = %q", got)
	}
	if got := responseError.Error(); got != "model response error" {
		t.Fatalf("Error() = %q", got)
	}
}
