package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/osama300/aurma-project/internal/store/storetest"
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
