package ids

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// iosAppIDRe mirrors what FirebaseCore checks on iOS: four parts, version 1, a decimal number,
// the literal platform "ios", and a hex tail that fits 64 bits.
var iosAppIDRe = regexp.MustCompile(`^1:[0-9]+:ios:[0-9a-f]{1,16}$`)

// androidKeyRe is the Android Installations SDK's API key check.
var androidKeyRe = regexp.MustCompile(`\AA[\w-]{38}\z`)

func TestGeneratedIdentifiersMatchTheirFormats(t *testing.T) {
	for i := 0; i < 2000; i++ {
		n := ProjectNumber()
		s := strconv.FormatInt(n, 10)
		if len(s) != 12 || s[0] == '0' {
			t.Fatalf("project number %d", n)
		}
		for _, p := range []Platform{Web, IOS, Android} {
			id := AppID(n, p)
			if !ValidAppID(id) {
				t.Fatalf("app id %q", id)
			}
			if got, ok := AppIDProjectNumber(id); !ok || got != n {
				t.Fatalf("AppIDProjectNumber(%q) = %d", id, got)
			}
		}
		if ios := AppID(n, IOS); !iosAppIDRe.MatchString(ios) {
			t.Fatalf("iOS would reject %q", ios)
		}
		k := APIKey()
		if len(k) != 39 || !strings.HasPrefix(k, "Aru") || strings.HasPrefix(k, "AIza") || !androidKeyRe.MatchString(k) || !ValidAPIKey(k) {
			t.Fatalf("api key %q", k)
		}
		u := ServiceAccountUniqueID()
		if len(u) != 21 || u[0] == '0' {
			t.Fatalf("unique id %q", u)
		}
		if kid := KeyID(); len(kid) != 40 || !hexRe.MatchString(kid) {
			t.Fatalf("key id %q", kid)
		}
	}
}

func TestProjectIDRules(t *testing.T) {
	good := []string{"chat-app", "abcdef", "a23456789012345678901234567890"}
	bad := []string{"chat", "Chat-App", "1chat-app", "chat-app-", "chat_app", "a234567890123456789012345678901", ""}
	for _, s := range good {
		if !ValidProjectID(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		if ValidProjectID(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestSuggestProjectID(t *testing.T) {
	cases := map[string]string{
		"Chat App":           "chat-app",
		"  My  Great--App! ": "my-great-app",
		"2048 Game":          "game-",
		"تطبيق الدردشة":      "project-",
		"x":                  "x-",
	}
	for name, prefix := range cases {
		got := SuggestProjectID(name, false)
		if !strings.HasPrefix(got, prefix) || !ValidProjectID(got) {
			t.Errorf("SuggestProjectID(%q) = %q, want prefix %q and valid", name, got, prefix)
		}
	}
	long := SuggestProjectID(strings.Repeat("very long name ", 10), true)
	if !ValidProjectID(long) {
		t.Errorf("long suggestion %q invalid", long)
	}
	if a, b := SuggestProjectID("Chat App", true), SuggestProjectID("Chat App", true); a == b {
		t.Error("suffixed suggestions should differ")
	}
}

func TestPackageAndBundleIDs(t *testing.T) {
	for _, s := range []string{"com.ahmad.chat", "a.b", "com.example.app_2"} {
		if !ValidPackageName(s) {
			t.Errorf("package %q should be valid", s)
		}
	}
	for _, s := range []string{"chat", "com..x", "1com.x", "com.x-y", "com.x."} {
		if ValidPackageName(s) {
			t.Errorf("package %q should be invalid", s)
		}
	}
	for _, s := range []string{"com.ahmad.chat", "com.ahmad.chat-dev"} {
		if !ValidBundleID(s) {
			t.Errorf("bundle %q should be valid", s)
		}
	}
	for _, s := range []string{"chat", "com ahmad", "com.ahmad_chat"} {
		if ValidBundleID(s) {
			t.Errorf("bundle %q should be invalid", s)
		}
	}
}

func TestNormalizeSHA(t *testing.T) {
	sha1 := "DA:39:A3:EE:5E:6B:4B:0D:32:55:BF:EF:95:60:18:90:AF:D8:07:09"
	h, typ, ok := NormalizeSHA(sha1)
	if !ok || typ != SHA1 || h != "da39a3ee5e6b4b0d3255bfef95601890afd80709" {
		t.Fatalf("NormalizeSHA = %q %q %v", h, typ, ok)
	}
	if _, typ, ok := NormalizeSHA(strings.Repeat("ab", 32)); !ok || typ != SHA256 {
		t.Fatal("SHA-256 not recognised")
	}
	for _, bad := range []string{"", "zz", strings.Repeat("a", 50)} {
		if _, _, ok := NormalizeSHA(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
