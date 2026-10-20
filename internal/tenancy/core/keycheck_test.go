package core

import (
	"encoding/json"
	"net/netip"
	"testing"
)

func TestReferrerPatterns(t *testing.T) {
	valid := []string{
		"example.com", "example.com/*", "*.example.com", "*.example.com/*", "www.example.com/path",
		"www.example.com/path/path", "https://example.com", "http://localhost:3000/*", "example.com:8080/app*",
		"http://127.0.0.1:5173/*", "http://[::1]:8080/*", "[2001:db8::1]/*", "chrome-extension://abcdef/*",
	}
	for _, p := range valid {
		if !ValidReferrerPattern(p) {
			t.Errorf("%q should be valid", p)
		}
	}
	invalid := []string{
		"", "*", "mysubdomain*.example.com", "example.*.com", "www.*.example.com/*", "example.com/*/path",
		"example.com/a*b", "https://", "://example.com", "1http://example.com", "example.com:", "example.com:80a",
		"example.com:*", "::1", "[::1", "user@example.com", "*.", "*.*.example.com",
	}
	for _, p := range invalid {
		if ValidReferrerPattern(p) {
			t.Errorf("%q should be invalid", p)
		}
	}
}

func TestMatchReferrer(t *testing.T) {
	cases := []struct {
		pattern, referrer string
		want              bool
	}{
		// "Allow any URL in your site": example.com/* and *.example.com/*.
		{"example.com/*", "https://example.com/", true},
		{"example.com/*", "https://example.com/a/b?q=1", true},
		{"example.com/*", "http://example.com/a", true},
		{"example.com/*", "https://www.example.com/", false},
		{"*.example.com/*", "https://www.example.com/x", true},
		{"*.example.com/*", "https://a.b.example.com/x", true},
		{"*.example.com/*", "https://example.com/x", false},
		{"*.example.com/*", "https://evil-example.com/x", false},
		{"*.example.com/*", "https://example.com.evil.com/x", false},
		{"*.example.com", "https://www.example.com/", true},
		{"*.example.com", "https://www.example.com/page", false},
		// A bare site matches its root only; the path wildcard is the second entry Google asks for.
		{"www.example.com", "https://www.example.com/", true},
		{"www.example.com", "https://www.example.com", true},
		{"www.example.com", "https://www.example.com/page", false},
		{"www.example.com", "https://WWW.Example.COM/", true},
		// An exact path, ignoring the query string.
		{"www.example.com/path", "https://www.example.com/path", true},
		{"www.example.com/path", "https://www.example.com/path?x=1", true},
		{"www.example.com/path", "https://www.example.com/path/", false},
		{"www.example.com/path", "https://www.example.com/Path", false},
		{"www.example.com/app*", "https://www.example.com/apps/1", true},
		{"www.example.com/app*", "https://www.example.com/ap", false},
		// Schemes: none in the pattern matches any.
		{"https://example.com/*", "https://example.com/x", true},
		{"https://example.com/*", "http://example.com/x", false},
		{"HTTPS://example.com/*", "https://example.com/x", true},
		// Ports: none in the pattern matches any; a given port must be the one used.
		{"example.com/*", "https://example.com:8443/", true},
		{"example.com:8443/*", "https://example.com:8443/", true},
		{"example.com:8443/*", "https://example.com/", false},
		{"example.com:443/*", "https://example.com/", true},
		{"example.com:80/*", "http://example.com/", true},
		{"http://localhost:3000/*", "http://localhost:3000/login", true},
		{"http://localhost:3000/*", "http://localhost:3001/login", false},
		// IP literals.
		{"http://127.0.0.1:5173/*", "http://127.0.0.1:5173/", true},
		{"http://[::1]:8080/*", "http://[0:0::1]:8080/", true},
		{"[2001:db8::1]/*", "https://[2001:db8::1]/x", true},
		// Missing or unusable referrers never match.
		{"example.com/*", "", false},
		{"example.com/*", "example.com/", false},
		{"example.com/*", "not a url", false},
		{"example.com/*", "https:///x", false},
		{"example.com/*", "about:blank", false},
		// Invalid patterns match nothing.
		{"*", "https://example.com/", false},
		{"my*.example.com/*", "https://myapp.example.com/", false},
	}
	for _, c := range cases {
		if got := MatchReferrer([]string{c.pattern}, c.referrer); got != c.want {
			t.Errorf("MatchReferrer(%q, %q) = %v, want %v", c.pattern, c.referrer, got, c.want)
		}
	}
	if !MatchReferrer([]string{"other.example/*", "example.com/*"}, "https://example.com/x") {
		t.Error("any of several patterns should match")
	}
	if MatchReferrer(nil, "https://example.com/") {
		t.Error("an empty list allows nothing")
	}
}

func TestMatchIP(t *testing.T) {
	allowed := []string{"198.51.100.1", "203.0.113.0/24", "2001:db8::/64", "2001:db8:1::5", " 192.0.2.7 ", "::ffff:10.1.0.0/112"}
	cases := []struct {
		ip   string
		want bool
	}{
		{"198.51.100.1", true},
		{"198.51.100.2", false},
		{"203.0.113.0", true},
		{"203.0.113.255", true},
		{"203.0.114.1", false},
		{"::ffff:203.0.113.9", true}, // a mapped IPv4 address is that IPv4 address
		{"2001:db8::1", true},
		{"2001:db8:0:0:ffff::1", true},
		{"2001:db8:0:1::1", false},
		{"2001:db8:1::5", true},
		{"2001:db8:1::6", false},
		{"192.0.2.7", true},
		{"10.1.200.3", true}, // a mapped CIDR range is the IPv4 range
		{"10.2.0.1", false},
	}
	for _, c := range cases {
		if got := MatchIP(allowed, netip.MustParseAddr(c.ip)); got != c.want {
			t.Errorf("MatchIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
	if MatchIP(allowed, netip.Addr{}) {
		t.Error("an unknown address never matches")
	}
	if MatchIP([]string{"not-an-ip", "10.0.0.0/33"}, netip.MustParseAddr("10.0.0.1")) {
		t.Error("invalid entries match nothing")
	}
	for _, bad := range []string{"", "10.0.0.256", "10.0.0.0/33", "fe80::1%en0", "example.com", "::ffff:10.0.0.0/90"} {
		if _, ok := parseIPEntry(bad); ok {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func restrictions(t *testing.T, raw string) Restrictions {
	t.Helper()
	var r Restrictions
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if err := r.validate(); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return r
}

func TestCheck(t *testing.T) {
	const itk, sts = ServiceIdentityToolkit, ServiceSecureToken
	ip := netip.MustParseAddr("198.51.100.7")
	const cert = "AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01"
	cases := []struct {
		name, restrictions string
		caller             KeyCaller
		reason, message    string
	}{
		{"unrestricted", `{}`, KeyCaller{Service: itk}, "", ""},
		{"unrestricted refresh", `{}`, KeyCaller{Service: sts}, "", ""},

		{"api target listed", `{"apiTargets":[{"service":"identitytoolkit.googleapis.com"}]}`, KeyCaller{Service: itk}, "", ""},
		{"api target case", `{"apiTargets":[{"service":"IdentityToolkit.googleapis.com"}]}`, KeyCaller{Service: itk}, "", ""},
		{"api target other", `{"apiTargets":[{"service":"firestore.googleapis.com"}]}`, KeyCaller{Service: itk},
			ReasonServiceBlocked, "Requests to this API identitytoolkit.googleapis.com are blocked."},
		{"refresh needs securetoken", `{"apiTargets":[{"service":"identitytoolkit.googleapis.com"}]}`, KeyCaller{Service: sts},
			ReasonServiceBlocked, "Requests to this API securetoken.googleapis.com are blocked."},
		{"refresh with securetoken", `{"apiTargets":[{"service":"identitytoolkit.googleapis.com"},{"service":"securetoken.googleapis.com"}]}`,
			KeyCaller{Service: sts}, "", ""},
		{"api target checked first", `{"apiTargets":[{"service":"firestore.googleapis.com"}],"browserKeyRestrictions":{"allowedReferrers":["good.example/*"]}}`,
			KeyCaller{Service: itk}, ReasonServiceBlocked, "Requests to this API identitytoolkit.googleapis.com are blocked."},

		{"referrer allowed", `{"browserKeyRestrictions":{"allowedReferrers":["https://good.example/*"]}}`,
			KeyCaller{Service: itk, Referrer: "https://good.example/app"}, "", ""},
		{"referrer other", `{"browserKeyRestrictions":{"allowedReferrers":["https://good.example/*"]}}`,
			KeyCaller{Service: itk, Referrer: "https://evil.example/"}, ReasonReferrerBlocked, "Requests from referer https://evil.example/ are blocked."},
		{"referrer missing", `{"browserKeyRestrictions":{"allowedReferrers":["https://good.example/*"]}}`,
			KeyCaller{Service: itk}, ReasonReferrerBlocked, "Requests from referer <empty> are blocked."},
		{"referrer list empty", `{"browserKeyRestrictions":{"allowedReferrers":[]}}`,
			KeyCaller{Service: itk, Referrer: "https://good.example/"}, ReasonReferrerBlocked, "Requests from referer https://good.example/ are blocked."},
		{"referrer on a page", `{"browserKeyRestrictions":{"allowedReferrers":["https://good.example/*"]}}`,
			KeyCaller{Service: itk, Referrer: "https://aruma.example/__/auth/action", Page: true}, ReasonReferrerBlocked,
			"Requests from referer https://aruma.example/__/auth/action are blocked."},

		{"ip allowed", `{"serverKeyRestrictions":{"allowedIps":["198.51.100.0/24"]}}`, KeyCaller{Service: itk, IP: ip}, "", ""},
		{"ip other", `{"serverKeyRestrictions":{"allowedIps":["203.0.113.1"]}}`, KeyCaller{Service: itk, IP: ip}, ReasonIPBlocked,
			"The provided API key has an IP address restriction. The originating IP address of the call (198.51.100.7) violates this restriction."},
		{"ip on a page", `{"serverKeyRestrictions":{"allowedIps":["203.0.113.1"]}}`, KeyCaller{Service: itk, IP: ip, Page: true}, ReasonIPBlocked,
			"The provided API key has an IP address restriction. The originating IP address of the call (198.51.100.7) violates this restriction."},

		{"android allowed", `{"androidKeyRestrictions":{"allowedApplications":[{"packageName":"com.example.app","sha1Fingerprint":"` + cert + `"}]}}`,
			KeyCaller{Service: itk, AndroidPackage: "com.example.app", AndroidCert: "ABCDEF0123456789ABCDEF0123456789ABCDEF01"}, "", ""},
		{"android cert lower case", `{"androidKeyRestrictions":{"allowedApplications":[{"packageName":"com.example.app","sha1Fingerprint":"` + cert + `"}]}}`,
			KeyCaller{Service: sts, AndroidPackage: "com.example.app", AndroidCert: "abcdef0123456789abcdef0123456789abcdef01"}, "", ""},
		{"android wrong cert", `{"androidKeyRestrictions":{"allowedApplications":[{"packageName":"com.example.app","sha1Fingerprint":"` + cert + `"}]}}`,
			KeyCaller{Service: itk, AndroidPackage: "com.example.app", AndroidCert: "0000000000000000000000000000000000000000"},
			ReasonAndroidBlocked, "Requests from this Android client application com.example.app are blocked."},
		{"android wrong package", `{"androidKeyRestrictions":{"allowedApplications":[{"packageName":"com.example.app","sha1Fingerprint":"` + cert + `"}]}}`,
			KeyCaller{Service: itk, AndroidPackage: "com.example.other", AndroidCert: "ABCDEF0123456789ABCDEF0123456789ABCDEF01"},
			ReasonAndroidBlocked, "Requests from this Android client application com.example.other are blocked."},
		{"android pair from two apps", `{"androidKeyRestrictions":{"allowedApplications":[
				{"packageName":"com.example.a","sha1Fingerprint":"` + cert + `"},
				{"packageName":"com.example.b","sha1Fingerprint":"0000000000000000000000000000000000000000"}]}}`,
			KeyCaller{Service: itk, AndroidPackage: "com.example.b", AndroidCert: "ABCDEF0123456789ABCDEF0123456789ABCDEF01"},
			ReasonAndroidBlocked, "Requests from this Android client application com.example.b are blocked."},
		{"android sha-256 cert", `{"androidKeyRestrictions":{"allowedApplications":[{"packageName":"com.example.app","sha1Fingerprint":"` + cert + `"}]}}`,
			KeyCaller{Service: itk, AndroidPackage: "com.example.app", AndroidCert: "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"},
			ReasonAndroidBlocked, "Requests from this Android client application com.example.app are blocked."},
		{"android headers missing", `{"androidKeyRestrictions":{"allowedApplications":[{"packageName":"com.example.app","sha1Fingerprint":"` + cert + `"}]}}`,
			KeyCaller{Service: itk}, ReasonAndroidBlocked, "Requests from this Android client application <empty> are blocked."},
		{"android on a page", `{"androidKeyRestrictions":{"allowedApplications":[{"packageName":"com.example.app","sha1Fingerprint":"` + cert + `"}]}}`,
			KeyCaller{Service: itk, Page: true}, "", ""},

		{"ios allowed", `{"iosKeyRestrictions":{"allowedBundleIds":["com.example.app"]}}`, KeyCaller{Service: itk, IOSBundleID: "com.example.app"}, "", ""},
		{"ios other", `{"iosKeyRestrictions":{"allowedBundleIds":["com.example.app"]}}`, KeyCaller{Service: itk, IOSBundleID: "com.example.other"},
			ReasonIOSBlocked, "Requests from this iOS client application com.example.other are blocked."},
		{"ios missing", `{"iosKeyRestrictions":{"allowedBundleIds":["com.example.app"]}}`, KeyCaller{Service: sts},
			ReasonIOSBlocked, "Requests from this iOS client application <empty> are blocked."},
		{"ios on a page", `{"iosKeyRestrictions":{"allowedBundleIds":["com.example.app"]}}`, KeyCaller{Service: itk, Page: true}, "", ""},
		{"ios page still needs the api", `{"iosKeyRestrictions":{"allowedBundleIds":["com.example.app"]},"apiTargets":[{"service":"firestore.googleapis.com"}]}`,
			KeyCaller{Service: itk, Page: true}, ReasonServiceBlocked, "Requests to this API identitytoolkit.googleapis.com are blocked."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := restrictions(t, c.restrictions).Check(c.caller)
			switch {
			case c.reason == "" && got != nil:
				t.Fatalf("refused: %s %s", got.Reason, got.Message)
			case c.reason == "":
			case got == nil:
				t.Fatalf("accepted, want %s", c.reason)
			case got.Reason != c.reason || got.Message != c.message:
				t.Fatalf("got %s %q, want %s %q", got.Reason, got.Message, c.reason, c.message)
			}
		})
	}
	blocked := restrictions(t, `{"androidKeyRestrictions":{"allowedApplications":[]}}`).Check(KeyCaller{Service: itk})
	if blocked == nil || blocked.Metadata["androidPackage"] != "<empty>" {
		t.Fatalf("android metadata: %+v", blocked)
	}
}

func TestRestrictionsValidate(t *testing.T) {
	bad := []string{
		`{"browserKeyRestrictions":{"allowedReferrers":["mysubdomain*.example.com"]}}`,
		`{"serverKeyRestrictions":{"allowedIps":["10.0.0.0/33"]}}`,
		`{"serverKeyRestrictions":{"allowedIps":["example.com"]}}`,
		`{"serverKeyRestrictions":{"allowedIps":[]},"iosKeyRestrictions":{"allowedBundleIds":[]}}`,
		`{"browserKeyRestrictions":{"allowedReferrers":[]},"serverKeyRestrictions":{"allowedIps":[]}}`,
		`{"apiTargets":[{"service":" "}]}`,
	}
	for _, raw := range bad {
		var r Restrictions
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		if r.validate() == nil {
			t.Errorf("%s should be rejected", raw)
		}
	}
	restrictions(t, `{"serverKeyRestrictions":{"allowedIps":["10.0.0.1","2001:db8::/64"]},"apiTargets":[{"service":"identitytoolkit.googleapis.com"}]}`)
}
