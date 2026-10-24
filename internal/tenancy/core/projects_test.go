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

// A bucket name resolves only in the form toProject derives (ADR-022 §2).
func TestBucketProjectID(t *testing.T) {
	s := &Service{opts: Options{ProjectDomain: "aruma.example.com"}}
	for bucket, want := range map[string]string{
		"chat-app.aruma.example.com":      "chat-app",
		"chat-app.aruma.example.com.":     "",
		"Chat-App.aruma.example.com":      "",
		"chat-app.ARUMA.example.com":      "",
		"chat-app.appspot.com":            "",
		"a.b.aruma.example.com":           "",
		".aruma.example.com":              "",
		"aruma.example.com":               "",
		"chat-app.aruma.example.com.evil": "",
		"x.aruma.example.com":             "",
	} {
		got, ok := s.bucketProjectID(bucket)
		if got != want || ok != (want != "") {
			t.Errorf("bucketProjectID(%q) = %q, %v; want %q", bucket, got, ok, want)
		}
	}
	if _, ok := (&Service{}).bucketProjectID("chat-app."); ok {
		t.Error("a bucket resolves without a project domain")
	}
}
