// Package log builds the server's structured JSON logger and keeps secrets out of it.
//
// Two layers protect secrets: values of type Secret always log as "[REDACTED]", and any
// attribute whose key names a credential (password, token, secret, cookie, ...) is redacted
// whatever its value. The second layer catches mistakes the first cannot.
package log

import (
	"io"
	"log/slog"
	"strings"
)

const redacted = "[REDACTED]"

// Secret wraps a sensitive string so it can never be logged by accident.
type Secret string

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// String keeps fmt from printing the value either.
func (Secret) String() string { return redacted }

// Reveal returns the underlying value for the code that actually needs it.
func (s Secret) Reveal() string { return string(s) }

// sensitiveKeys are matched against attribute keys, case-insensitively, as substrings.
var sensitiveKeys = []string{
	"password", "passwd", "secret", "token", "authorization", "cookie",
	"private_key", "privatekey", "master_key", "masterkey", "csrf", "assertion", "credential",
}

// IsSensitiveKey reports whether an attribute key names a credential.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// New returns a JSON logger writing to w at the given level ("debug", "info", "warn", "error").
func New(w io.Writer, level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       lv,
		ReplaceAttr: redact,
	}))
}

func redact(_ []string, a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, redacted)
	}
	return a
}
