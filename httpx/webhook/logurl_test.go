package webhook

import "testing"

func TestLogURL_RedactsPathAndQuery(t *testing.T) {
	got := logURL("https://hooks.slack.com/services/T1/B2/secret-token?token=abc")
	if got != "https://hooks.slack.com" {
		t.Fatalf("logURL = %q, want scheme://host only", got)
	}
}

func TestLogURL_InvalidReturnsRedacted(t *testing.T) {
	if got := logURL("://bad"); got != "[redacted]" {
		t.Fatalf("logURL invalid = %q, want [redacted]", got)
	}
	if got := logURL("/relative/path/secret"); got != "[redacted]" {
		t.Fatalf("logURL relative = %q, want [redacted]", got)
	}
}
