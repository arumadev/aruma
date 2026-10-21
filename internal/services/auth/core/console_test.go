package core

import (
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/auth/queries"
)

func TestCheckConfigDomains(t *testing.T) {
	c := queries.AuthProjectConfig{AuthorizedDomains: []string{"localhost", " Chat.Example.COM", "chat.example.com", "10.0.0.2", "::1", "xn--mgbh0fb.example"}}
	if err := checkConfig(&c); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.AuthorizedDomains, " "); got != "localhost chat.example.com 10.0.0.2 ::1 xn--mgbh0fb.example" {
		t.Fatalf("normalised: %s", got)
	}
	for _, bad := range []string{"", "https://example.com", "example.com/", "example.com:443", "*.example.com", "-a.example.com",
		"a..example.com", "مثال.example", "fe80::1%en0", strings.Repeat("a", 64) + ".com"} {
		c := queries.AuthProjectConfig{AuthorizedDomains: []string{bad}}
		if err := checkConfig(&c); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	many := queries.AuthProjectConfig{AuthorizedDomains: make([]string, maxAuthorizedDomains+1)}
	if err := checkConfig(&many); err == nil {
		t.Error("too many domains accepted")
	}
	long := queries.AuthProjectConfig{AppName: strings.Repeat("ا", 257)}
	if err := checkConfig(&long); err == nil {
		t.Error("long app name accepted")
	}
}
