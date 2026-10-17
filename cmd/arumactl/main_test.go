package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	arumaapp "github.com/osama300/aurma-project/internal/app"
	"github.com/osama300/aurma-project/internal/config"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/store/storetest"
	"github.com/osama300/aurma-project/internal/tenancy/core"
)

// server starts Aruma on a fresh database and returns its URL and a CLI token.
func server(t *testing.T) (string, string) {
	t.Helper()
	pool := storetest.NewPool(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	vars := map[string]string{"ARUMA_DATABASE_URL": "postgres://unused", "ARUMA_PUBLIC_URL": base, "ARUMA_MASTER_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	cfg, err := config.Load("", func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	a, err := arumaapp.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", pool, arumaapp.WithArgon2(crypto.Argon2Params{Memory: 64, Time: 1, Threads: 1}))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(a.Server.Handler())
	ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)

	ctx := context.Background()
	acc, err := a.Service.CreateAccount(ctx, core.NewAccount{Email: "dev@example.com", Password: "correct horse battery"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.Service.CreateAccessToken(ctx, &authn.Principal{Kind: authn.KindAccount, AccountID: acc.ID}, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	return base, tok.Plaintext
}

type harness struct {
	t   *testing.T
	env map[string]string
}

func (h *harness) run(stdin string, args ...string) (string, error) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	err := run(args, func(k string) string { return h.env[k] }, strings.NewReader(stdin), &out, &errOut)
	return out.String(), err
}

func (h *harness) must(stdin string, args ...string) string {
	h.t.Helper()
	out, err := h.run(stdin, args...)
	if err != nil {
		h.t.Fatalf("arumactl %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func TestDeveloperFlow(t *testing.T) {
	base, token := server(t)
	dir := t.TempDir()
	h := &harness{t: t, env: map[string]string{"ARUMACTL_CONFIG": filepath.Join(dir, "credentials.json")}}

	if _, err := h.run("", "projects", "list"); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("before login: %v", err)
	}
	if _, err := h.run("aruma_pat_wrong\n", "login", "--server", base); err == nil {
		t.Fatal("login accepted a wrong token")
	}
	if out := h.must(token+"\n", "login", "--server", base); !strings.Contains(out, "dev@example.com") {
		t.Fatalf("login: %s", out)
	}
	if fi, err := os.Stat(filepath.Join(dir, "credentials.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file: %v %v", fi, err)
	}

	h.must("", "projects", "create", "chat-app", "--name", "Chat App")
	var projects struct {
		Projects []project `json:"projects"`
	}
	json.Unmarshal([]byte(h.must("", "--json", "projects", "list")), &projects)
	if len(projects.Projects) != 1 || projects.Projects[0].ProjectID != "chat-app" {
		t.Fatalf("projects: %+v", projects)
	}

	var web app
	json.Unmarshal([]byte(h.must("", "--json", "apps", "create", "--project", "chat-app", "--platform", "web", "--name", "Web")), &web)
	h.must("", "apps", "create", "--project", "chat-app", "--platform", "android", "--package", "com.ahmad.chat")
	if out := h.must("", "apps", "list", "--project", "chat-app", "--platform", "android"); !strings.Contains(out, "com.ahmad.chat") {
		t.Fatalf("apps list: %s", out)
	}
	if out := h.must("", "apps", "sdkconfig", "--project", "chat-app", web.AppID); !strings.Contains(out, `"appId": "`+web.AppID+`"`) {
		t.Fatalf("sdkconfig: %s", out)
	}
	h.must("", "apps", "sdkconfig", web.AppID, "--project", "chat-app", "--out", dir)
	if _, err := os.Stat(filepath.Join(dir, "firebase-config.json")); err != nil {
		t.Fatal("sdkconfig --out dir did not use the server's file name")
	}

	var keys struct {
		Keys []apiKey `json:"keys"`
	}
	json.Unmarshal([]byte(h.must("", "--json", "apikeys", "list", "--project", "chat-app")), &keys)
	if len(keys.Keys) != 2 {
		t.Fatalf("keys: %+v", keys)
	}
	h.must("", "apikeys", "rotate", "--project", "chat-app", keys.Keys[0].UID)

	h.must("", "sa", "create", "--project", "chat-app", "backend")
	keyFile := filepath.Join(dir, "key.json")
	h.must("", "sa", "keys", "create", "--project", "chat-app", "backend@chat-app.sa.127.0.0.1", "--out", keyFile)
	fi, err := os.Stat(keyFile)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", fi, err)
	}
	b, _ := os.ReadFile(keyFile)
	if !bytes.Contains(b, []byte(`"type": "service_account"`)) {
		t.Fatalf("key file: %s", b)
	}
	if _, err := h.run("", "sa", "keys", "create", "--project", "chat-app", "backend"); err == nil {
		t.Fatal("key creation without --out must be refused")
	}

	if _, err := h.run("", "projects", "get", "nope-project"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("server error not surfaced: %v", err)
	}
	h.must("", "logout")
	if _, err := h.run("", "projects", "list"); err == nil {
		t.Fatal("still logged in after logout")
	}
}

// Review L-6: secret files are private even when they already existed with wider modes, and
// a symlink at the path is replaced rather than followed.
func TestWritePrivate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	os.WriteFile(target, []byte("keep"), 0o644)
	p := filepath.Join(dir, "key.json")
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(p, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("followed the symlink")
	}
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v", fi.Mode(), err)
	}
}
