package core

import "testing"

// TestDatabaseURL pins the two databaseURL forms of ADR-018 that config files carry.
func TestDatabaseURL(t *testing.T) {
	for _, c := range []struct {
		opts Options
		want string
	}{
		{Options{PublicURL: "https://aruma.example.com", RTDBDomain: "rtdb.example.com"}, "https://chat-app-default-rtdb.rtdb.example.com"},
		{Options{PublicURL: "https://aruma.example.com", RTDBDomain: "example-db.com"}, "https://chat-app-default-rtdb.example-db.com"},
		// Without rtdb.domain: the development form on the main host.
		{Options{PublicURL: "http://127.0.0.1:8080"}, "http://127.0.0.1:8080/?ns=chat-app-default-rtdb"},
	} {
		if got := (&Service{opts: c.opts}).DatabaseURL("chat-app"); got != c.want {
			t.Errorf("DatabaseURL with %+v = %q, want %q", c.opts, got, c.want)
		}
	}
}
