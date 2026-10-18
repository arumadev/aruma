package acceptance

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
)

// X-7 / ADR-010 S-15: after a full scenario, no secret appears in the database or the log in a
// usable form: no password, session, CSRF, CLI or service account token, no private key, no
// master key.
func TestNoSecretsInDatabaseOrLogs(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.owner()
	owner.project("chat-app")
	owner.app("chat-app", "WEB", "")
	owner.app("chat-app", "ANDROID", "com.ahmad.chat")
	pat := owner.pat()
	cli := e.withBearer(pat)
	cli.mustOK(cli.do("GET", "/v1beta1/projects", nil))
	owner.mustOK(owner.do("POST", "/console/v1/me:changePassword", map[string]any{"currentPassword": testPassword, "newPassword": "a second strong password"}))
	_, keyFile := owner.serviceAccountKey("chat-app", "backend")
	saToken, _, err := exchange(t, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	e.withBearer(saToken).mustOK(e.withBearer(saToken).do("GET", "/v1beta1/projects", nil))
	// A failed login carries a password too.
	e.client().do("POST", "/console/v1/auth:login", map[string]any{"email": "owner@example.com", "password": "typo-in-the-password"})

	block, _ := pem.Decode([]byte(parseKeyFile(t, keyFile).PrivateKey))
	secrets := map[string]string{
		"first password":        testPassword,
		"second password":       "a second strong password",
		"mistyped password":     "typo-in-the-password",
		"session token":         owner.cookies["__Host-aruma_session"].Value,
		"CSRF token":            owner.csrf,
		"CLI token":             pat,
		"service account token": saToken,
		"private key (base64)":  base64.StdEncoding.EncodeToString(block.Bytes)[64:128],
		"master key":            testMasterKey,
	}
	for name, s := range secrets {
		if s == "" {
			t.Fatalf("scenario did not produce a %s", name)
		}
	}

	dump := dumpDatabase(t, e)
	logs := e.Logs.String()
	// Guard against a vacuous pass: the dump and the log must hold the scenario's public data.
	if !strings.Contains(dump, "chat-app") || !strings.Contains(dump, "owner@example.com") || !strings.Contains(logs, `"msg":"request"`) {
		t.Fatal("database dump or log is missing the scenario's public data")
	}
	for name, s := range secrets {
		if strings.Contains(dump, s) {
			t.Errorf("%s found in the database", name)
		}
		if strings.Contains(logs, s) {
			t.Errorf("%s found in the server log", name)
		}
	}
	if strings.Contains(dump, "PRIVATE KEY") || strings.Contains(logs, "PRIVATE KEY") {
		t.Error("a PEM private key was stored or logged")
	}
}

// dumpDatabase renders every row of every Aruma table as text.
func dumpDatabase(t *testing.T, e *Env) string {
	t.Helper()
	ctx := context.Background()
	rows, err := e.Pool.Query(ctx, `SELECT schemaname || '.' || tablename FROM pg_tables WHERE schemaname IN ('auth', 'control', 'events', 'public')`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	var b strings.Builder
	for _, table := range tables {
		r, err := e.Pool.Query(ctx, fmt.Sprintf(`SELECT t::text FROM %s t`, table))
		if err != nil {
			t.Fatal(err)
		}
		for r.Next() {
			var line string
			r.Scan(&line)
			b.WriteString(line)
			b.WriteByte('\n')
		}
		r.Close()
	}
	return b.String()
}
