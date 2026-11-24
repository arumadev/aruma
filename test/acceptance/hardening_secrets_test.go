package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/crypto"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// Hardening: the existing secrets test dumps the control and auth schemas after a console
// scenario. This one runs a scenario through every service, then dumps every schema, Storage,
// the Realtime Database and the Document Database included, and the log, and looks for each
// credential in the form it was issued.
func TestHardNoCredentialsAtRestOrInLogs(t *testing.T) {
	h := newHardEnv(t, nil)
	p := h.b
	h.open(p, "bravo-secret")

	// Credentials in every position a client sends them, so that a log or an error that echoes a
	// URL, a header or a body would show.
	secrets := map[string]string{
		"owner password":        testPassword,
		"CLI token":             p.pat,
		"service account token": p.sat,
		"ID token signature":    strings.Split(p.idToken, ".")[2],
		"refresh token":         p.refresh,
		"user password":         "secret-alice",
		"API key restriction":   "",
	}
	delete(secrets, "API key restriction")
	for _, c := range []struct{ method, path string }{
		{"GET", "/secret.json?ns=" + p.ns + "&auth=" + url.QueryEscape(p.idToken)},
		{"GET", "/secret.json?ns=" + p.ns + "&access_token=" + url.QueryEscape(p.sat)},
		{"GET", "/v0/b/" + p.bucket + "/o/secret.txt?alt=media&token=notarealtoken-0123456789"},
	} {
		restAs(t, h.Env, c.method, c.path, "", "", nil)
	}
	// A password reset, a custom token, a Storage upload session and its download token.
	ok(t, h.Env.client().do("POST", itk+"accounts:sendOobCode?key="+p.key, map[string]any{"requestType": "PASSWORD_RESET", "email": "alice@example.com"}))
	custom := customToken(t, p.saKey, "carol", nil, nil)
	secrets["custom token signature"] = strings.Split(custom, ".")[2]
	ok(t, h.Env.client().do("POST", itk+"accounts:signInWithCustomToken?key="+p.key, map[string]any{"token": custom, "returnSecureToken": true}))
	req, _ := http.NewRequest("POST", h.URL+"/v0/b/"+p.bucket+"/o?uploadType=resumable&name=up%2Ffile.bin", nil)
	req.Header.Set("Authorization", "Bearer "+p.sat)
	req.Header.Set("X-Goog-Upload-Protocol", "resumable")
	req.Header.Set("X-Goog-Upload-Command", "start")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	uploadURL := resp.Header.Get("X-Goog-Upload-URL")
	if m := regexp.MustCompile(`upload_id=([^&]+)`).FindStringSubmatch(uploadURL); m != nil {
		secrets["upload session id"] = m[1]
	} else {
		t.Fatalf("no upload session: %d %v", resp.StatusCode, resp.Header)
	}
	bucket, err := h.App.Storage.Bucket(t.Context(), p.bucket)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := h.App.Storage.Insert(t.Context(), bucket, storage.Principal{Kind: storage.PrincipalAdmin},
		storage.UploadSpec{Name: "tok.txt", Declared: 3, Surface: storage.SurfaceFirebase}, strings.NewReader("abc"), 3)
	if err != nil || len(tok.DownloadTokens) != 1 {
		t.Fatalf("object with a download token: %v %v", tok.DownloadTokens, err)
	}
	secrets["download token"] = tok.DownloadTokens[0]
	if len(secrets) < 8 {
		t.Fatalf("scenario produced too few credentials: %d", len(secrets))
	}

	dump := dumpEverything(t, h.Env)
	logs := h.Logs.String()
	if !strings.Contains(dump, "bravo-app") || !strings.Contains(dump, "secret.txt") || !strings.Contains(logs, `"msg":"request"`) {
		t.Fatal("the dump or the log lacks the scenario's public data")
	}
	for name, s := range secrets {
		if s == "" {
			t.Errorf("scenario produced no %s", name)
			continue
		}
		if strings.Contains(dump, s) {
			t.Errorf("%s is stored in clear", name)
		}
		if strings.Contains(logs, s) {
			t.Errorf("%s is in the server log", name)
		}
	}
	if strings.Contains(dump, "PRIVATE KEY") || strings.Contains(logs, "PRIVATE KEY") {
		t.Error("a PEM private key was stored or logged")
	}
	// Every stored password is an argon2id hash, and a fresh install hashes with at least
	// OWASP's minimum: 19 MiB, two passes, one lane.
	for _, row := range queryColumn(t, h.Env, `SELECT hash FROM auth.user_passwords UNION ALL SELECT password_hash FROM control.accounts`) {
		if !strings.HasPrefix(row, "$argon2id$v=19$") {
			t.Errorf("a stored password is not an argon2id hash: %.30s", row)
		}
	}
	if d := crypto.DefaultArgon2; d.Memory < 19*1024 || d.Time < 2 || d.Threads < 1 {
		t.Errorf("default argon2id parameters %+v are below OWASP's minimum (m=19456, t=2, p=1)", d)
	}
	// Tokens are kept as 32-byte HMACs, never in the clear.
	for _, q := range []string{
		`SELECT length(token_hash) FROM auth.refresh_tokens`, `SELECT length(token_hash) FROM control.sessions`,
		`SELECT length(token_hash) FROM control.access_tokens`, `SELECT length(token_hash) FROM control.oauth_access_tokens`,
		`SELECT length(id_hash) FROM storage.upload_sessions`, `SELECT length(code_hash) FROM auth.oob_codes`,
	} {
		rows := queryColumn(t, h.Env, q)
		if len(rows) == 0 {
			t.Errorf("%s: no rows", q)
		}
		for _, n := range rows {
			if n != "32" {
				t.Errorf("%s: %s bytes, want a 32-byte HMAC", q, n)
			}
		}
	}
}

// dumpEverything renders every row of every table outside the system schemas as text.
func dumpEverything(t *testing.T, e *Env) string {
	t.Helper()
	ctx := context.Background()
	rows, err := e.Pool.Query(ctx, `SELECT schemaname || '.' || quote_ident(tablename) FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema')`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	var b strings.Builder
	for _, table := range tables {
		r, err := e.Pool.Query(ctx, fmt.Sprintf(`SELECT t::text FROM %s t`, table))
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		for r.Next() {
			var line string
			_ = r.Scan(&line)
			b.WriteString(line)
			b.WriteByte('\n')
		}
		r.Close()
	}
	return b.String()
}

func queryColumn(t *testing.T, e *Env, query string) []string {
	t.Helper()
	rows, err := e.Pool.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(v))
	}
	return out
}
