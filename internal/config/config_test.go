package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func required() map[string]string {
	return map[string]string{
		"ARUMA_DATABASE_URL": "postgres://localhost/aruma",
		"ARUMA_PUBLIC_URL":   "https://aruma.example.com",
		"ARUMA_MASTER_KEY":   testKey,
	}
}

func TestEnvOnly(t *testing.T) {
	c, err := Load("", env(required()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.SignupOpen || c.ProjectDomain != "aruma.example.com" {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if len(c.MasterKeyBytes()) != 32 || !c.SecureCookies() {
		t.Fatal("derived values wrong")
	}
}

func TestMissingRequired(t *testing.T) {
	_, err := Load("", env(nil))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"ARUMA_DATABASE_URL", "ARUMA_PUBLIC_URL", "ARUMA_MASTER_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestEnvOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aruma.yaml")
	yaml := `
database_url: postgres://file/aruma
public_url: http://localhost:8080
master_key: ` + testKey + `
signup_open: true
trusted_proxies: ["10.0.0.0/8", "127.0.0.1"]
session:
  idle_timeout: 10m
  absolute_timeout: 2h
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, env(map[string]string{"ARUMA_SIGNUP_OPEN": "false", "ARUMA_METRICS_LISTEN": "-"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.SignupOpen {
		t.Error("env did not override signup_open")
	}
	if c.MetricsListen != "" {
		t.Error(`"-" should disable metrics_listen`)
	}
	if c.Session.IdleTimeout != 10*time.Minute || len(c.TrustedProxyPrefixes()) != 2 {
		t.Errorf("file values lost: %+v", c)
	}
	if !c.SecureCookies() {
		t.Error("localhost should use secure cookies")
	}
}

func TestRejectsUnknownFileKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aruma.yaml")
	os.WriteFile(path, []byte("signup_opn: true\n"), 0o600)
	if _, err := Load(path, env(required())); err == nil {
		t.Fatal("typo in config key should fail")
	}
}

func TestRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"ARUMA_MASTER_KEY":           "c2hvcnQ=",
		"ARUMA_PUBLIC_URL":           "aruma.example.com",
		"ARUMA_LOG_LEVEL":            "verbose",
		"ARUMA_TRUSTED_PROXIES":      "not-an-ip",
		"ARUMA_SESSION_IDLE_TIMEOUT": "48h",
	}
	for k, v := range cases {
		m := required()
		m[k] = v
		if _, err := Load("", env(m)); err == nil {
			t.Errorf("%s=%q should be rejected", k, v)
		}
	}
}

func TestPlainHTTPHostIsNotSecure(t *testing.T) {
	m := required()
	m["ARUMA_PUBLIC_URL"] = "http://aruma.lan:8080"
	c, err := Load("", env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.SecureCookies() {
		t.Error("plain http on a LAN host cannot use Secure cookies")
	}
}
