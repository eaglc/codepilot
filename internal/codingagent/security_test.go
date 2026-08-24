package codingagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecurityPolicyClassifiesBuiltInAndConfiguredSensitivePaths(t *testing.T) {
	policy, err := NewSecurityPolicy([]string{"config/private", `Local\Vault`})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	for _, path := range []string{
		".env", ".env.production", "keys/server.pem", "id_ed25519", ".aws/credentials", ".docker/config.json",
		".kube/config", ".ssh/config", "config/private/token.txt", "local/vault/item.json", "service-account-prod.json",
	} {
		if !policy.IsSensitivePath(path) {
			t.Errorf("sensitive path %q was not classified", path)
		}
	}
	for _, path := range []string{".env.example", ".env.sample", "config/public.json", "src/key.go"} {
		if policy.IsSensitivePath(path) {
			t.Errorf("ordinary path %q was classified sensitive", path)
		}
	}
	if _, err := NewSecurityPolicy([]string{"../outside"}); err == nil {
		t.Fatal("traversing custom sensitive path was accepted")
	}
}

func TestSecurityPolicyRedactsTextAndJSONButPreservesSafeJSONBytes(t *testing.T) {
	policy, _ := NewSecurityPolicy(nil)
	privateKey := "-----BEGIN PRIVATE KEY-----\nprivate-material\n-----END PRIVATE KEY-----"
	input := "API_KEY=top-secret-value\nAuthorization: Bearer abcdefghijklmnop\nhttps://user:password@example.test\n" + privateKey + "\nsk-1234567890abcdefghijkl"
	redacted := policy.RedactText(input)
	for _, secret := range []string{"top-secret-value", "abcdefghijklmnop", "password@example", "private-material", "sk-1234567890abcdefghijkl"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted text retained %q: %q", secret, redacted)
		}
	}
	if strings.Count(redacted, RedactedValue) < 5 {
		t.Fatalf("redacted text = %q", redacted)
	}
	safe := json.RawMessage("{ \"path\" : \"main.go\", \"count\" : 2 }")
	if got := policy.RedactJSON(safe); string(got) != string(safe) {
		t.Fatalf("safe JSON bytes changed: got %q want %q", got, safe)
	}
	secretJSON := json.RawMessage(`{"path":"main.go","token":"top-secret-value","nested":{"password":"hunter22"}}`)
	got := policy.RedactJSON(secretJSON)
	if strings.Contains(string(got), "top-secret-value") || strings.Contains(string(got), "hunter22") || !json.Valid(got) {
		t.Fatalf("secret JSON was not safely redacted: %s", got)
	}
}

func TestSecurityTextStreamSanitizerStreamsSafePrefixesAndRedactsSplitSecrets(t *testing.T) {
	policy, err := NewSecurityPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	sanitizer := policy.NewTextStreamSanitizer()
	chunks := []string{
		strings.Repeat("ordinary words remain visible while streaming. ", 4),
		" TOKEN=top-",
		"secret and the response continues safely.",
	}
	var streamed strings.Builder
	first := sanitizer.Write(chunks[0])
	if first == "" {
		t.Fatal("safe text was buffered until the response finished")
	}
	streamed.WriteString(first)
	for _, chunk := range chunks[1:] {
		streamed.WriteString(sanitizer.Write(chunk))
	}
	streamed.WriteString(sanitizer.Flush())
	raw := strings.Join(chunks, "")
	if got, want := streamed.String(), policy.RedactText(raw); got != want || strings.Contains(got, "top-secret") {
		t.Fatalf("streamed redaction = %q want %q", got, want)
	}
	chinese := policy.NewTextStreamSanitizer()
	if first := chinese.Write(strings.Repeat("这是流式中文内容。", 10)); first == "" {
		t.Fatal("unspaced CJK text was buffered until the response finished")
	}

	private := policy.NewTextStreamSanitizer()
	privateChunks := []string{
		"safe prefix " + strings.Repeat("text ", 20) + "-----BEGIN PRIVATE KEY-----\nprivate-",
		"material\n-----END PRIVATE KEY----- " + strings.Repeat("safe tail ", 20),
	}
	streamed.Reset()
	for _, chunk := range privateChunks {
		streamed.WriteString(private.Write(chunk))
	}
	streamed.WriteString(private.Flush())
	privateRaw := strings.Join(privateChunks, "")
	if got, want := streamed.String(), policy.RedactText(privateRaw); got != want || strings.Contains(got, "private-material") {
		t.Fatalf("streamed private-key redaction = %q want %q", got, want)
	}
}
