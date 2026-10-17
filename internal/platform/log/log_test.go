package log

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretsNeverReachTheLog(t *testing.T) {
	const leak = "s3cr3t-value-that-must-not-appear"
	var buf bytes.Buffer
	l := New(&buf, "debug")

	l.Info("login", "password", leak)
	l.Info("request", slog.Group("headers", "Authorization", "Bearer "+leak, "Cookie", leak))
	l.Info("token issued", "access_token", leak, "csrfToken", leak, "private_key", leak)
	l.Info("wrapped", "value", Secret(leak))
	l.Info("nested", slog.Group("account", "id", "42", "secret", Secret(leak)))
	l.Info(fmt.Sprintf("formatted %v", Secret(leak)))

	out := buf.String()
	if strings.Contains(out, leak) {
		t.Fatalf("secret leaked into log:\n%s", out)
	}
	if !strings.Contains(out, `"id":"42"`) {
		t.Fatalf("non-secret attribute was lost:\n%s", out)
	}
}

func TestLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "warn")
	l.Info("hidden")
	l.Warn("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Fatalf("level not applied: %s", buf.String())
	}
}

func TestIsSensitiveKey(t *testing.T) {
	for _, k := range []string{"password_hash", "X-Aruma-CSRF", "refreshToken", "MASTER_KEY"} {
		if !IsSensitiveKey(k) {
			t.Errorf("%q should be sensitive", k)
		}
	}
	for _, k := range []string{"project_id", "email", "path", "status"} {
		if IsSensitiveKey(k) {
			t.Errorf("%q should not be sensitive", k)
		}
	}
}
