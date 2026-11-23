package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

func aruma(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return out.String() + errOut.String(), err
}

func TestOperatorAccountCommands(t *testing.T) {
	t.Setenv("ARUMA_DATABASE_URL", storetest.NewDB(t))
	t.Setenv("ARUMA_PUBLIC_URL", "http://localhost:8080")
	t.Setenv("ARUMA_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")

	if out, err := aruma(t, "a good password\n", "accounts", "create", "--email", "ops@example.com", "--display-name", "Ops"); err != nil || !strings.Contains(out, "created account ops@example.com") {
		t.Fatalf("create: %v %s", err, out)
	}
	if _, err := aruma(t, "a good password\n", "accounts", "create", "--email", "ops@example.com"); err == nil {
		t.Fatal("duplicate account created")
	}
	if _, err := aruma(t, "short\n", "accounts", "create", "--email", "x@example.com"); err == nil {
		t.Fatal("short password accepted")
	}
	for _, cmd := range []string{"disable", "enable"} {
		if out, err := aruma(t, "", "accounts", cmd, "--email", "ops@example.com"); err != nil || !strings.Contains(out, cmd+"d") {
			t.Fatalf("%s: %v %s", cmd, err, out)
		}
	}
	if _, err := aruma(t, "another password\n", "accounts", "reset-password", "--email", "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := aruma(t, "", "accounts", "disable", "--email", "nobody@example.com"); err == nil || !strings.Contains(err.Error(), "No account") {
		t.Fatalf("unknown account: %v", err)
	}
	if _, err := aruma(t, "", "accounts", "create"); err == nil {
		t.Fatal("missing --email accepted")
	}
}

// A database a newer aruma migrated: status says so, and up refuses instead of reporting
// success with nothing applied.
func TestMigrateNewerSchema(t *testing.T) {
	url := storetest.NewDB(t)
	t.Setenv("ARUMA_DATABASE_URL", url)
	pool, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO public.goose_auth (version_id, is_applied) SELECT max(version_id) + 1, true FROM public.goose_auth"); err != nil {
		t.Fatal(err)
	}

	out, err := aruma(t, "", "migrate", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		newer := strings.HasSuffix(line, "newer than this binary")
		if strings.HasPrefix(line, "auth ") != newer {
			t.Errorf("status line %q", line)
		}
	}
	if _, err := aruma(t, "", "migrate", "up"); !errors.Is(err, store.ErrSchemaNewer) {
		t.Fatalf("migrate up = %v, want ErrSchemaNewer", err)
	}
}

func TestCommandValidation(t *testing.T) {
	cases := [][]string{
		{},
		{"nope"},
		{"restore", "--in", "x.dump"}, // without --yes
		{"backup"},
		{"migrate", "sideways"},
		{"config", "verify"},
	}
	t.Setenv("ARUMA_DATABASE_URL", "postgres://127.0.0.1:1/none")
	for _, args := range cases {
		if _, err := aruma(t, "", args...); err == nil {
			t.Errorf("aruma %v: expected an error", args)
		}
	}
	if out, err := aruma(t, "", "version"); err != nil || strings.TrimSpace(out) != "dev" {
		t.Fatalf("version: %q %v", out, err)
	}
}

// The operator commands read database_url from the configuration file as serve does: a
// deployment configured by a file alone (the minimal production example) migrates and backs up
// without ARUMA_DATABASE_URL.
func TestOperatorCommandsReadTheConfigFile(t *testing.T) {
	url := storetest.NewDB(t)
	t.Setenv("ARUMA_DATABASE_URL", "")
	file := filepath.Join(t.TempDir(), "aruma.yaml")
	if err := os.WriteFile(file, []byte("database_url: "+url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := aruma(t, "", "--config", file, "migrate", "up"); err != nil || !strings.Contains(out, "up to date") {
		t.Fatalf("migrate up from the file: %v %s", err, out)
	}
	if out, err := aruma(t, "", "--config", file, "migrate", "status"); err != nil || strings.Contains(out, "pending") {
		t.Fatalf("migrate status from the file: %v %s", err, out)
	}
	if _, err := aruma(t, "", "migrate", "status"); err == nil || !strings.Contains(err.Error(), "database_url") {
		t.Fatalf("migrate without a database URL: %v", err)
	}
}
