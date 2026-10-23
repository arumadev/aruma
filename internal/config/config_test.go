package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/rules"
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

func TestDevMailboxDefaultsToLoopback(t *testing.T) {
	for _, c := range []struct {
		url, setting string
		want         bool
	}{
		{"http://127.0.0.1:8080", "", true},
		{"http://localhost:8080", "", true},
		{"https://aruma.example.com", "", false},
		{"https://aruma.example.com", "true", true},
		{"http://localhost:8080", "false", false},
	} {
		m := required()
		m["ARUMA_PUBLIC_URL"] = c.url
		m["ARUMA_MAIL_DEV_MAILBOX"] = c.setting
		cfg, err := Load("", env(m))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.DevMailbox(); got != c.want {
			t.Errorf("%s with %q: DevMailbox() = %v, want %v", c.url, c.setting, got, c.want)
		}
	}
	m := required()
	m["ARUMA_MAIL_DEV_MAILBOX"] = "maybe"
	if _, err := Load("", env(m)); err == nil {
		t.Error("a non-boolean ARUMA_MAIL_DEV_MAILBOX should be rejected")
	}
}

func TestSMTPAllowPrivateDefaultsToLoopback(t *testing.T) {
	for _, c := range []struct {
		url, setting string
		want         bool
	}{
		{"http://127.0.0.1:8080", "", true},
		{"https://aruma.example.com", "", false},
		{"https://aruma.example.com", "true", true},
		{"http://localhost:8080", "false", false},
	} {
		m := required()
		m["ARUMA_PUBLIC_URL"] = c.url
		m["ARUMA_MAIL_SMTP_ALLOW_PRIVATE"] = c.setting
		cfg, err := Load("", env(m))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.SMTPAllowPrivate(); got != c.want {
			t.Errorf("%s with %q: SMTPAllowPrivate() = %v, want %v", c.url, c.setting, got, c.want)
		}
	}
}

func TestRTDBDefaultsOff(t *testing.T) {
	c, err := Load("", env(required()))
	if err != nil {
		t.Fatal(err)
	}
	if c.RTDB.Enabled || c.RTDB.DevOpenRules || c.RTDB.Domain != "" {
		t.Errorf("RTDB must be off and locked by default: %+v", c.RTDB)
	}
	if c.RTDB.Limits.MessageBytes != 16<<20 || c.RTDB.Limits.OutboxBytes != 64<<20 || c.RTDB.Limits.ConnectionsPerProject != 10000 ||
		c.RTDB.Limits.ReadRows != 1_000_000 {
		t.Errorf("RTDB limits do not follow ADR-019: %+v", c.RTDB.Limits)
	}
}

func TestRTDBSettings(t *testing.T) {
	m := required()
	m["ARUMA_PUBLIC_URL"] = "http://127.0.0.1:8080"
	m["ARUMA_RTDB_ENABLED"] = "true"
	m["ARUMA_RTDB_DOMAIN"] = "RTDB.Example.com."
	m["ARUMA_RTDB_DEV_OPEN_RULES"] = "true"
	m["ARUMA_LISTEN"] = "127.0.0.1:8080"
	m["ARUMA_RTDB_ALLOWED_ORIGINS"] = "App.Example.com, localhost"
	m["ARUMA_RTDB_OUTBOX_BYTES"] = "2097152"
	m["ARUMA_RTDB_CONNECTIONS_PER_PROJECT"] = "5"
	m["ARUMA_RTDB_CONNECTIONS_PER_IP"] = "7"
	m["ARUMA_RTDB_UNAUTHENTICATED_MESSAGE_BYTES"] = "4096"
	m["ARUMA_RTDB_INFLIGHT_BYTES"] = "2097152"
	m["ARUMA_RTDB_READ_ROWS"] = "5000"
	c, err := Load("", env(m))
	if err != nil {
		t.Fatal(err)
	}
	l := c.RTDB.Limits
	if !c.RTDB.Enabled || !c.RTDB.DevOpenRules || c.RTDB.Domain != "rtdb.example.com" ||
		l.OutboxBytes != 2<<20 || l.ConnectionsPerProject != 5 || l.ConnectionsPerIP != 7 ||
		l.UnauthenticatedMessageBytes != 4096 || l.InflightBytes != 2<<20 || l.ReadRows != 5000 ||
		strings.Join(c.RTDB.AllowedOrigins, ",") != "app.example.com,localhost" {
		t.Errorf("settings not applied: %+v", c.RTDB)
	}
}

func TestRTDBRejectsBadValues(t *testing.T) {
	cases := []map[string]string{
		{"ARUMA_RTDB_DOMAIN": "https://rtdb.example.com"},
		{"ARUMA_RTDB_DOMAIN": "rtdb.example.com:443"},
		{"ARUMA_RTDB_DOMAIN": "localhost"},
		{"ARUMA_RTDB_DOMAIN": "under_score.example.com"},
		// Open rules on a public server would expose every project's data.
		{"ARUMA_RTDB_DEV_OPEN_RULES": "true"},
		// A loopback public_url is not enough: the listener must be loopback too (review #2).
		{"ARUMA_RTDB_DEV_OPEN_RULES": "true", "ARUMA_PUBLIC_URL": "http://127.0.0.1:8080"},
		{"ARUMA_RTDB_DEV_OPEN_RULES": "true", "ARUMA_PUBLIC_URL": "http://127.0.0.1:8080", "ARUMA_LISTEN": "0.0.0.0:8080"},
		// Aruma's own hosts may not lie under rtdb.domain (review #7).
		{"ARUMA_RTDB_DOMAIN": "example.com", "ARUMA_PUBLIC_URL": "https://aruma.example.com"},
		{"ARUMA_RTDB_DOMAIN": "aruma.example.com", "ARUMA_PUBLIC_URL": "https://aruma.example.com"},
		{"ARUMA_RTDB_DOMAIN": "rtdb.example.com", "ARUMA_PROJECT_DOMAIN": "apps.rtdb.example.com"},
		{"ARUMA_RTDB_ALLOWED_ORIGINS": "https://app.example.com/"},
		{"ARUMA_RTDB_MESSAGE_BYTES": "1024"},
		{"ARUMA_RTDB_OUTBOX_BYTES": "x"},
		{"ARUMA_RTDB_CONNECTIONS_PER_PROJECT": "0"},
		{"ARUMA_RTDB_CONNECTIONS_PER_IP": "0"},
		{"ARUMA_RTDB_READ_ROWS": "10"},
		{"ARUMA_RTDB_UNAUTHENTICATED_MESSAGE_BYTES": "999999999"},
	}
	for _, c := range cases {
		m := required()
		for k, v := range c {
			m[k] = v
		}
		if _, err := Load("", env(m)); err == nil {
			t.Errorf("%v should be rejected", c)
		}
	}
	// rtdb.domain beside Aruma's hosts is fine.
	m := required()
	m["ARUMA_RTDB_DOMAIN"] = "rtdb.example.com"
	if _, err := Load("", env(m)); err != nil {
		t.Errorf("rtdb.example.com beside aruma.example.com: %v", err)
	}
}

func TestRulesLimitsDefaultToADR021(t *testing.T) {
	c, err := Load("", env(required()))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.RulesEngineLimits(); got != rules.DefaultLimits() {
		t.Errorf("rules limits = %+v, want rules.DefaultLimits()", got)
	}
	if l := c.Rules.Limits; l.InflightBytes != 512<<20 || l.CompiledRulesetsBytes != 128<<20 || l.CallDepth != 20 {
		t.Errorf("rules limits do not follow ADR-021: %+v", l)
	}
}

// Every rules limit has a file key and an ARUMA_RULES_* variable named after it, and the
// variable wins. The file lowers every limit by one, which every limit accepts, and the
// variables lower every other one by one more.
func TestRulesLimitsSettings(t *testing.T) {
	fields := reflect.VisibleFields(reflect.TypeFor[RulesLimits]())
	def := reflect.ValueOf(Defaults().Rules.Limits)
	var yaml strings.Builder
	yaml.WriteString("database_url: postgres://file/aruma\npublic_url: https://aruma.example.com\nmaster_key: " + testKey + "\nrules:\n  limits:\n")
	vars := map[string]string{}
	for i, f := range fields {
		key, n := f.Tag.Get("yaml"), def.Field(i).Int()
		fmt.Fprintf(&yaml, "    %s: %d\n", key, n-1)
		if i%2 == 0 {
			vars["ARUMA_RULES_"+strings.ToUpper(key)] = strconv.FormatInt(n-2, 10)
		}
	}
	path := filepath.Join(t.TempDir(), "aruma.yaml")
	if err := os.WriteFile(path, []byte(yaml.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, env(vars))
	if err != nil {
		t.Fatal(err)
	}
	got := reflect.ValueOf(c.Rules.Limits)
	for i, f := range fields {
		want := def.Field(i).Int() - 1
		if i%2 == 0 {
			want--
		}
		if got.Field(i).Int() != want {
			t.Errorf("%s (ARUMA_RULES_%s) = %d, want %d", f.Tag.Get("yaml"), strings.ToUpper(f.Tag.Get("yaml")), got.Field(i).Int(), want)
		}
	}
	if rules.Limits(c.Rules.Limits) != c.RulesEngineLimits() {
		t.Error("RulesEngineLimits does not return the configured limits")
	}
}

func TestRulesLimitsRejectBadValues(t *testing.T) {
	for _, c := range []struct {
		name, value, want string
	}{
		// Raising a Firebase limit would allow what Firebase denies.
		{"ARUMA_RULES_CALL_DEPTH", "21", "rules.limits.call_depth 21 is above Firebase's 20"},
		{"ARUMA_RULES_EXPRESSION_DEPTH", "99", "rules.limits.expression_depth"},
		{"ARUMA_RULES_LOOKUPS_PER_REQUEST", "21", "rules.limits.lookups_per_request"},
		{"ARUMA_RULES_STORAGE_LOOKUPS", "3", "rules.limits.storage_lookups"},
		{"ARUMA_RULES_SOURCE_BYTES", "1000000", "rules.limits.source_bytes"},
		{"ARUMA_RULES_INFLIGHT_BYTES", "0", "rules.limits.inflight_bytes must be at least 1"},
		{"ARUMA_RULES_WORK", "-5", "rules.limits.work must be at least 1"},
		{"ARUMA_RULES_MATCH_DEPTH", "ten", "ARUMA_RULES_MATCH_DEPTH"},
	} {
		m := required()
		m[c.name] = c.value
		_, err := Load("", env(m))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s=%s: error %v, want one containing %q", c.name, c.value, err, c.want)
		}
	}
	// Aruma's limits have no ceiling.
	m := required()
	m["ARUMA_RULES_WORK"] = "1099511627776"
	m["ARUMA_RULES_INFLIGHT_BYTES"] = "1099511627776"
	m["ARUMA_RULES_MEMBER_CHAIN"] = "100000"
	if _, err := Load("", env(m)); err != nil {
		t.Errorf("raised Aruma limits: %v", err)
	}
}
