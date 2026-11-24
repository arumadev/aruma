package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Retention defaults to 90 days of audit and 30 days of change log; 0 keeps a table forever.
func TestRetentionSettings(t *testing.T) {
	c, err := Load("", env(required()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention != (Retention{AuthAuditDays: 90, ControlAuditDays: 90, EventsDays: 30}) {
		t.Errorf("defaults = %+v", c.Retention)
	}
	vars := required()
	vars["ARUMA_RETENTION_AUTH_AUDIT_DAYS"] = "0"
	vars["ARUMA_RETENTION_CONTROL_AUDIT_DAYS"] = "7"
	vars["ARUMA_RETENTION_EVENTS_DAYS"] = "365"
	if c, err = Load("", env(vars)); err != nil {
		t.Fatal(err)
	}
	if c.Retention != (Retention{AuthAuditDays: 0, ControlAuditDays: 7, EventsDays: 365}) {
		t.Errorf("from the environment = %+v", c.Retention)
	}
	path := filepath.Join(t.TempDir(), "aruma.yaml")
	if err := os.WriteFile(path, []byte("retention:\n  auth_audit_days: 14\n  control_audit_days: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(path, env(required())); err != nil {
		t.Fatal(err)
	}
	if c.Retention != (Retention{AuthAuditDays: 14, ControlAuditDays: 0, EventsDays: 30}) {
		t.Errorf("from the file = %+v", c.Retention)
	}
}

func TestRetentionRejectsBadValues(t *testing.T) {
	for name, want := range map[string]map[string]string{
		"retention.auth_audit_days":    {"ARUMA_RETENTION_AUTH_AUDIT_DAYS": "-1"},
		"retention.events_days":        {"ARUMA_RETENTION_EVENTS_DAYS": "36501"},
		"retention.control_audit_days": {"ARUMA_RETENTION_CONTROL_AUDIT_DAYS": "6"},
		"ARUMA_RETENTION_EVENTS_DAYS":  {"ARUMA_RETENTION_EVENTS_DAYS": "30d"},
	} {
		vars := required()
		for k, v := range want {
			vars[k] = v
		}
		if _, err := Load("", env(vars)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%v: err = %v, want one naming %s", want, err, name)
		}
	}
}
