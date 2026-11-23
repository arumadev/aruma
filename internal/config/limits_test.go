package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The server timeouts and Auth rate limits default to the values they had when they were fixed.
func TestServerAndAuthLimitSettings(t *testing.T) {
	c, err := Load("", env(required()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != (Server{ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, WriteTimeout: time.Minute, IdleTimeout: 2 * time.Minute}) {
		t.Errorf("server defaults = %+v", c.Server)
	}
	if c.Auth.Limits != (AuthLimits{RequestsPerMinute: 300, RequestBurst: 100, SignUpsPerMinute: 2, SignUpBurst: 100}) {
		t.Errorf("auth defaults = %+v", c.Auth.Limits)
	}
	vars := required()
	vars["ARUMA_SERVER_WRITE_TIMEOUT"] = "5m"
	vars["ARUMA_AUTH_REQUESTS_PER_MINUTE"] = "1200"
	vars["ARUMA_AUTH_SIGN_UP_BURST"] = "5"
	if c, err = Load("", env(vars)); err != nil {
		t.Fatal(err)
	}
	if c.Server.WriteTimeout != 5*time.Minute || c.Auth.Limits.RequestsPerMinute != 1200 || c.Auth.Limits.SignUpBurst != 5 || c.Auth.Limits.RequestBurst != 100 {
		t.Errorf("from the environment: %+v %+v", c.Server, c.Auth.Limits)
	}
	path := filepath.Join(t.TempDir(), "aruma.yaml")
	if err := os.WriteFile(path, []byte("server:\n  idle_timeout: 30s\nauth:\n  limits:\n    sign_ups_per_minute: 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(path, env(required())); err != nil {
		t.Fatal(err)
	}
	if c.Server.IdleTimeout != 30*time.Second || c.Auth.Limits.SignUpsPerMinute != 10 {
		t.Errorf("from the file: %+v %+v", c.Server, c.Auth.Limits)
	}
}

func TestServerAndAuthLimitsRejectBadValues(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"server.read_header_timeout":                     {"ARUMA_SERVER_READ_HEADER_TIMEOUT": "0s"},
		"server.read_timeout":                            {"ARUMA_SERVER_READ_TIMEOUT": "5s"},
		"server.write_timeout":                           {"ARUMA_SERVER_WRITE_TIMEOUT": "20s"},
		"server.idle_timeout":                            {"ARUMA_SERVER_IDLE_TIMEOUT": "2h"},
		"read_header_timeout must not exceed":            {"ARUMA_SERVER_READ_HEADER_TIMEOUT": "30s", "ARUMA_SERVER_READ_TIMEOUT": "20s"},
		"auth.limits.requests_per_minute":                {"ARUMA_AUTH_REQUESTS_PER_MINUTE": "0"},
		"auth.limits.request_burst":                      {"ARUMA_AUTH_REQUEST_BURST": "-1"},
		"auth.limits.sign_ups_per_minute":                {"ARUMA_AUTH_SIGN_UPS_PER_MINUTE": "1000001"},
		"auth.limits.sign_up_burst":                      {"ARUMA_AUTH_SIGN_UP_BURST": "0"},
		"ARUMA_SERVER_WRITE_TIMEOUT: time: invalid":      {"ARUMA_SERVER_WRITE_TIMEOUT": "sixty"},
		"ARUMA_AUTH_REQUEST_BURST: strconv.ParseInt: pa": {"ARUMA_AUTH_REQUEST_BURST": "lots"},
	} {
		all := required()
		for k, v := range vars {
			all[k] = v
		}
		if _, err := Load("", env(all)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%v: err = %v, want one naming %q", vars, err, name)
		}
	}
}
