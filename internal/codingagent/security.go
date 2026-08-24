package codingagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const RedactedValue = "[REDACTED]"

var (
	privateKeyPattern    = regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	tokenPattern         = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{16,}|gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|xox[baprs]-[a-z0-9-]{16,}|AKIA[0-9A-Z]{16})\b`)
	bearerPattern        = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._~+/=-]{12,}`)
	assignmentPattern    = regexp.MustCompile(`(?im)((?:api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|password|passwd|credential|secret|token)\s*(?:=|:)\s*["']?)([^"'\s,;]{4,})`)
	credentialURLPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^\s/:@]+:)([^\s/@]+)(@)`)
)

// SecurityPolicy classifies worktree paths and removes recognized secret
// values before data crosses a model, journal, artifact, event, or UI boundary.
type SecurityPolicy struct {
	customPaths []string
}

// NewSecurityPolicy validates exact file/directory prefixes configured by the user.
func NewSecurityPolicy(customPaths []string) (*SecurityPolicy, error) {
	normalized, err := NormalizeSensitivePaths(customPaths)
	if err != nil {
		return nil, err
	}
	return &SecurityPolicy{customPaths: normalized}, nil
}

// WithSensitivePaths returns an immutable policy extended with session paths.
func (p *SecurityPolicy) WithSensitivePaths(values []string) (*SecurityPolicy, error) {
	combined := append([]string(nil), values...)
	if p != nil {
		combined = append(combined, p.customPaths...)
	}
	return NewSecurityPolicy(combined)
}

// NormalizeSensitivePaths validates persisted user-configured path prefixes.
func NormalizeSensitivePaths(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		original := strings.TrimSpace(value)
		value = strings.ReplaceAll(original, "\\", "/")
		value = strings.TrimSuffix(value, "/")
		clean := path.Clean(value)
		if original == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || strings.ContainsAny(clean, "\x00\r\n:") || clean != value || len(clean) > 4096 {
			return nil, errors.New("sensitive paths must be normalized worktree-relative file or directory paths")
		}
		clean = strings.ToLower(clean)
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		normalized = append(normalized, clean)
	}
	sort.Strings(normalized)
	return normalized, nil
}

// ValidateSensitivePaths ensures persisted paths are already canonical.
func ValidateSensitivePaths(values []string) error {
	normalized, err := NormalizeSensitivePaths(values)
	if err != nil || len(normalized) != len(values) {
		if err != nil {
			return err
		}
		return errors.New("sensitive paths must be unique and normalized")
	}
	for index := range normalized {
		if normalized[index] != values[index] {
			return errors.New("sensitive paths must be sorted normalized worktree-relative paths")
		}
	}
	return nil
}

// IsSensitivePath reports whether a relative path is a built-in or configured secret source.
func (p *SecurityPolicy) IsSensitivePath(value string) bool {
	value = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "\\", "/")))
	value = strings.TrimPrefix(path.Clean(value), "./")
	if value == "" || value == "." {
		return false
	}
	for _, configured := range p.paths() {
		if value == configured || strings.HasPrefix(value, configured+"/") {
			return true
		}
	}
	base := path.Base(value)
	if base == ".env.example" || base == ".env.sample" || base == ".env.template" {
		return false
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == ".netrc" || base == ".npmrc" || base == ".pypirc" || base == ".git-credentials" || base == "credentials" || base == "credentials.json" || base == "auth.json" || strings.HasPrefix(base, "secrets.") || strings.HasPrefix(base, "service-account") || base == "id_rsa" || base == "id_dsa" || base == "id_ecdsa" || base == "id_ed25519" {
		return true
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".pem", ".key", ".p12", ".pfx", ".ppk", ".jks", ".keystore":
		return true
	}
	return value == ".docker/config.json" || value == ".aws/credentials" || value == ".kube/config" || strings.HasPrefix(value, ".azure/") || strings.HasPrefix(value, ".ssh/")
}

// ContainsSecret reports whether text would be changed by deterministic redaction.
func (p *SecurityPolicy) ContainsSecret(value string) bool { return p.RedactText(value) != value }

// RedactText removes recognized credentials while preserving surrounding diagnostics.
func (*SecurityPolicy) RedactText(value string) string {
	value = privateKeyPattern.ReplaceAllString(value, RedactedValue)
	value = tokenPattern.ReplaceAllString(value, RedactedValue)
	value = bearerPattern.ReplaceAllString(value, `${1}`+RedactedValue)
	value = assignmentPattern.ReplaceAllString(value, `${1}`+RedactedValue)
	value = credentialURLPattern.ReplaceAllString(value, `${1}`+RedactedValue+`${3}`)
	return value
}

// RedactJSON preserves the original bytes when no value is sensitive. This is
// required so safe durable Tool arguments keep their recovery digest semantics.
func (p *SecurityPolicy) RedactJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return append(json.RawMessage(nil), raw...)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return append(json.RawMessage(nil), raw...)
	}
	redacted, changed := p.redactJSONValue("", value)
	if !changed {
		return append(json.RawMessage(nil), raw...)
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	return encoded
}

// SanitizeMessage implements the generic Agent durable-data policy.
func (p *SecurityPolicy) SanitizeMessage(message llm.Message) llm.Message {
	message = message.Clone()
	for index := range message.Content {
		content := &message.Content[index]
		if content.Type == llm.ContentText || content.Type == llm.ContentThinking {
			content.Text = p.RedactText(content.Text)
		}
		if content.Type == llm.ContentToolCall && content.ToolCall != nil {
			content.ToolCall.Arguments = p.RedactJSON(content.ToolCall.Arguments)
		}
	}
	message.Details = p.RedactJSON(message.Details)
	return message
}

// SanitizeToolArguments implements the generic Agent durable-data policy.
func (p *SecurityPolicy) SanitizeToolArguments(_ string, arguments json.RawMessage) json.RawMessage {
	return p.RedactJSON(arguments)
}

// SanitizeToolResult implements the generic Agent durable-data policy.
func (p *SecurityPolicy) SanitizeToolResult(_ string, result tool.Result) tool.Result {
	result = result.Clone()
	for index := range result.Content {
		if result.Content[index].Type == llm.ContentText || result.Content[index].Type == llm.ContentThinking {
			result.Content[index].Text = p.RedactText(result.Content[index].Text)
		}
	}
	result.Details = p.RedactJSON(result.Details)
	return result
}

// SanitizeText implements the generic Agent error/event policy.
func (p *SecurityPolicy) SanitizeText(value string) string { return p.RedactText(value) }

// NewTextStreamSanitizer keeps only the small suffix that could still become
// part of a recognized credential, allowing earlier safe text to render while
// the model response is still arriving.
func (p *SecurityPolicy) NewTextStreamSanitizer() agent.TextStreamSanitizer {
	return &securityTextStreamSanitizer{policy: p}
}

const streamingRedactionTailBytes = 16

type securityTextStreamSanitizer struct {
	policy  *SecurityPolicy
	pending string
}

func (s *securityTextStreamSanitizer) Write(value string) string {
	s.pending += value
	return s.drain(false)
}

func (s *securityTextStreamSanitizer) Flush() string { return s.drain(true) }

func (s *securityTextStreamSanitizer) drain(final bool) string {
	if s.pending == "" {
		return ""
	}
	if final {
		value := s.policy.RedactText(s.pending)
		s.pending = ""
		return value
	}
	if len(s.pending) <= streamingRedactionTailBytes {
		return ""
	}
	cut := streamSafeCut(s.pending, len(s.pending)-streamingRedactionTailBytes)
	if begin := strings.LastIndex(strings.ToUpper(s.pending[:cut]), "-----BEGIN "); begin >= 0 {
		match := privateKeyPattern.FindStringIndex(s.pending[begin:])
		if match == nil || begin+match[1] > cut {
			cut = begin
		}
	}
	if cut <= 0 {
		return ""
	}
	value := s.policy.RedactText(s.pending[:cut])
	s.pending = s.pending[cut:]
	return value
}

// streamSafeCut retains the candidate credential fragment plus enough prior
// ASCII fragments for prefixes such as "API_KEY =" and "Bearer". Natural
// language runes (including unspaced CJK text) do not become an unbounded tail.
func streamSafeCut(value string, candidate int) int {
	candidate = min(max(0, candidate), len(value))
	for candidate > 0 && candidate < len(value) && !utf8.RuneStart(value[candidate]) {
		candidate--
	}
	for token := 0; token < 4; token++ {
		for candidate > 0 {
			r, size := utf8.DecodeLastRuneInString(value[:candidate])
			if !streamCredentialRune(r) {
				break
			}
			candidate -= size
		}
		for candidate > 0 {
			r, size := utf8.DecodeLastRuneInString(value[:candidate])
			if !unicodeSpace(r) {
				break
			}
			candidate -= size
		}
	}
	return candidate
}

func streamCredentialRune(value rune) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("._~+/=:@'\"-_", value)
}

func unicodeSpace(value rune) bool {
	switch value {
	case ' ', '\t', '\r', '\n', '\v', '\f':
		return true
	default:
		return false
	}
}

func (p *SecurityPolicy) paths() []string {
	if p == nil {
		return nil
	}
	return p.customPaths
}

func (p *SecurityPolicy) redactJSONValue(key string, value any) (any, bool) {
	if sensitiveJSONKey(key) {
		return RedactedValue, true
	}
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for childKey, child := range typed {
			redacted, childChanged := p.redactJSONValue(childKey, child)
			if childChanged {
				typed[childKey], changed = redacted, true
			}
		}
		return typed, changed
	case []any:
		changed := false
		for index := range typed {
			redacted, childChanged := p.redactJSONValue("", typed[index])
			if childChanged {
				typed[index], changed = redacted, true
			}
		}
		return typed, changed
	case string:
		redacted := p.RedactText(typed)
		return redacted, redacted != typed
	default:
		return value, false
	}
}

func sensitiveJSONKey(value string) bool {
	value = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", "_"), " ", "_"))
	for _, fragment := range []string{"password", "passwd", "secret", "credential", "api_key", "apikey", "token", "access_token", "auth_token", "refresh_token", "private_key", "authorization"} {
		if value == fragment || strings.HasSuffix(value, "_"+fragment) {
			return true
		}
	}
	return false
}

var defaultSecurityPolicy = &SecurityPolicy{}

// RedactSensitiveText applies the default output/error redaction policy.
func RedactSensitiveText(value string) string { return defaultSecurityPolicy.RedactText(value) }

func redactSensitiveText(value string) string { return RedactSensitiveText(value) }
