package probe

import (
	"strings"
	"testing"
)

func TestRedactor(t *testing.T) {
	redactor := NewDefaultRedactor()

	tests := []struct {
		name     string
		input    string
		contains string // what should NOT be present in output
	}{
		{
			name:     "private ipv4 class A",
			input:    "Service running on 10.0.1.25:8080",
			contains: "10.0.1.25",
		},
		{
			name:     "private ipv4 class C",
			input:    "Database at 192.168.1.100",
			contains: "192.168.1.100",
		},
		{
			name:     "private ipv4 class B",
			input:    "Internal gateway 172.20.5.1",
			contains: "172.20.5.1",
		},
		{
			name:     "openai standard api key",
			input:    "sk-abcdefghijklmnopqrstuvwxyz1234567890",
			contains: "sk-abcdefghijklmnopqrstuvwxyz1234567890",
		},
		{
			name:     "openai project api key",
			input:    "sk-proj-abcdefghijklmnopqrstuvwxyz1234567890",
			contains: "sk-proj-abcdefghijklmnopqrstuvwxyz1234567890",
		},
		{
			name:     "github classic token",
			input:    "ghp_1234567890abcdefghijklmnopqrstuvwxyz",
			contains: "ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		},
		{
			name:     "github fine grained token",
			input:    "github_pat_11AABCDEF0123456789_abcdefghijklmnopqrstuvwxyz",
			contains: "github_pat_11AABCDEF0123456789_abcdefghijklmnopqrstuvwxyz",
		},
		{
			name:     "aws access key id",
			input:    "AKIAIOSFODNN7EXAMPLE",
			contains: "AKIAIOSFODNN7EXAMPLE",
		},
		{
			name:     "bearer token",
			input:    "Authorization: Bearer secret_token_value_longer_than_20_chars",
			contains: "secret_token_value_longer_than_20_chars",
		},
		{
			name:     "jwt token",
			input:    "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			contains: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactor.Redact(tc.input)
			if strings.Contains(got, tc.contains) {
				t.Errorf("expected %q to be redacted from %q, got: %q", tc.contains, tc.input, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("expected [REDACTED] in output, got: %q", got)
			}
		})
	}
}

func TestTruncateAnswer(t *testing.T) {
	short := "Hello world"
	if TruncateAnswer(short) != short {
		t.Errorf("expected short answer unchanged")
	}

	long := strings.Repeat("A", MaxAnswerBytes+100)
	truncated := TruncateAnswer(long)

	if len(truncated) <= MaxAnswerBytes {
		t.Errorf("expected truncated string length to include marker, got len %d", len(truncated))
	}
	if !strings.HasSuffix(truncated, " [truncated by TalkIntent daemon]") {
		t.Errorf("expected truncation marker at end of output, got: %s", truncated[len(truncated)-40:])
	}
	prefix := strings.TrimSuffix(truncated, " [truncated by TalkIntent daemon]")
	if len(prefix) != MaxAnswerBytes {
		t.Errorf("expected prefix to be exactly %d bytes, got %d", MaxAnswerBytes, len(prefix))
	}
}
