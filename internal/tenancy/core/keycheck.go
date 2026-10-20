package core

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/platform/ids"
)

// The services a data plane names when it checks a key's API restrictions, as Google names them.
const (
	ServiceIdentityToolkit = "identitytoolkit.googleapis.com"
	ServiceSecureToken     = "securetoken.googleapis.com"
)

// KeyCaller is what a request tells about its sender, read the way Google reads it for API key
// restrictions: the Referer header from browsers, X-Android-Package and X-Android-Cert from
// Android apps, X-Ios-Bundle-Identifier from iOS apps, and the client address.
type KeyCaller struct {
	Service        string // the API called, e.g. identitytoolkit.googleapis.com
	Referrer       string
	IP             netip.Addr
	AndroidPackage string
	AndroidCert    string // SHA-1 of the signing certificate, hex, any case, colons allowed
	IOSBundleID    string
	// Page marks a page Aruma serves itself (the email action and sign-in handler pages), whose
	// Referrer is the page's own URL. A browser cannot say which app opened it, so Android and
	// iOS restrictions are not checked there; the calls that hand out tokens still are.
	Page bool
}

// CallerOf reads a client request's KeyCaller. The address is the one the server already
// resolved, through trusted proxies only.
func CallerOf(r *http.Request, service string) KeyCaller {
	return KeyCaller{
		Service:        service,
		Referrer:       r.Header.Get("Referer"),
		IP:             httpx.ClientAddr(r.Context()),
		AndroidPackage: r.Header.Get("X-Android-Package"),
		AndroidCert:    r.Header.Get("X-Android-Cert"),
		IOSBundleID:    r.Header.Get("X-Ios-Bundle-Identifier"),
	}
}

// KeyBlocked is a request that a key's restrictions refuse. Reason is the google.api.ErrorReason
// Google sends in the ErrorInfo detail and Message its text; Metadata adds what Google adds
// besides the consumer and the service.
type KeyBlocked struct {
	Reason   string
	Message  string
	Metadata map[string]string
}

func (e *KeyBlocked) Error() string { return e.Message }

// The reasons of KeyBlocked (google/api/error_reason.proto).
const (
	ReasonServiceBlocked  = "API_KEY_SERVICE_BLOCKED"
	ReasonReferrerBlocked = "API_KEY_HTTP_REFERRER_BLOCKED"
	ReasonIPBlocked       = "API_KEY_IP_ADDRESS_BLOCKED"
	ReasonAndroidBlocked  = "API_KEY_ANDROID_APP_BLOCKED"
	ReasonIOSBlocked      = "API_KEY_IOS_APP_BLOCKED"
)

const empty = "<empty>"

func orEmpty(s string) string {
	if s == "" {
		return empty
	}
	return s
}

// Check applies the restrictions to a caller and returns nil when the key may be used. A key
// without restrictions is accepted from anywhere for any service. API restrictions are checked
// first, then the one kind of application restriction the key carries.
func (r Restrictions) Check(c KeyCaller) *KeyBlocked {
	if len(r.APITargets) > 0 && !r.allowsService(c.Service) {
		return &KeyBlocked{Reason: ReasonServiceBlocked, Message: "Requests to this API " + c.Service + " are blocked."}
	}
	switch {
	case r.BrowserKeyRestrictions != nil:
		if !MatchReferrer(r.BrowserKeyRestrictions.AllowedReferrers, c.Referrer) {
			return &KeyBlocked{Reason: ReasonReferrerBlocked, Message: "Requests from referer " + orEmpty(c.Referrer) + " are blocked."}
		}
	case r.ServerKeyRestrictions != nil:
		if !MatchIP(r.ServerKeyRestrictions.AllowedIPs, c.IP) {
			ip := empty
			if c.IP.IsValid() {
				ip = c.IP.Unmap().String()
			}
			return &KeyBlocked{Reason: ReasonIPBlocked,
				Message: "The provided API key has an IP address restriction. The originating IP address of the call (" + ip + ") violates this restriction."}
		}
	case r.AndroidKeyRestrictions != nil && !c.Page:
		if !r.allowsAndroid(c.AndroidPackage, c.AndroidCert) {
			return &KeyBlocked{Reason: ReasonAndroidBlocked, Message: "Requests from this Android client application " + orEmpty(c.AndroidPackage) + " are blocked.",
				Metadata: map[string]string{"androidPackage": orEmpty(c.AndroidPackage)}}
		}
	case r.IOSKeyRestrictions != nil && !c.Page:
		if !r.allowsIOS(c.IOSBundleID) {
			return &KeyBlocked{Reason: ReasonIOSBlocked, Message: "Requests from this iOS client application " + orEmpty(c.IOSBundleID) + " are blocked."}
		}
	}
	return nil
}

// allowsService reports whether the API targets list the service. Service names are case
// insensitive, as in Google's ApiTarget.
func (r Restrictions) allowsService(service string) bool {
	for _, t := range r.APITargets {
		if strings.EqualFold(strings.TrimSpace(t.Service), service) {
			return true
		}
	}
	return false
}

func (r Restrictions) allowsAndroid(pkg, cert string) bool {
	got, typ, ok := ids.NormalizeSHA(cert)
	if pkg == "" || !ok || typ != ids.SHA1 {
		return false
	}
	for _, app := range r.AndroidKeyRestrictions.AllowedApplications {
		want, _, _ := ids.NormalizeSHA(app.SHA1Fingerprint)
		if app.PackageName == pkg && want == got {
			return true
		}
	}
	return false
}

func (r Restrictions) allowsIOS(bundleID string) bool {
	for _, b := range r.IOSKeyRestrictions.AllowedBundleIDs {
		if bundleID != "" && b == bundleID {
			return true
		}
	}
	return false
}

// MatchIP reports whether ip is one of the allowed addresses or lies in one of the allowed
// CIDR ranges. IPv4 addresses mapped into IPv6 count as IPv4.
func MatchIP(allowed []string, ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	for _, a := range allowed {
		if p, ok := parseIPEntry(a); ok && p.Contains(ip) {
			return true
		}
	}
	return false
}

// parseIPEntry reads an allowed IP entry: an address or a CIDR range, IPv4 or IPv6.
func parseIPEntry(s string) (netip.Prefix, bool) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		if p.Addr().Is4In6() {
			bits := p.Bits() - 96
			if bits < 0 {
				return netip.Prefix{}, false
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), bits)
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), true
}

// referrerPattern is an allowed referrer, in the forms Google documents for website
// restrictions: an optional scheme, a host that may start with "*." for any subdomain, an
// optional port and an optional path that may end with "*". A wildcard anywhere else makes the
// pattern invalid.
type referrerPattern struct {
	scheme    string // "" matches any scheme
	host      string // lower case, without the "*." prefix
	subdomain bool   // "*.host": any host below host, not host itself
	port      string // "" matches any port
	path      string // "" matches the site's root only
	prefix    bool   // the path ended with "*"
}

func parseReferrerPattern(s string) (referrerPattern, bool) {
	var p referrerPattern
	s = strings.TrimSpace(s)
	if scheme, rest, ok := strings.Cut(s, "://"); ok {
		if !validScheme(scheme) {
			return p, false
		}
		p.scheme, s = strings.ToLower(scheme), rest
	}
	hostport, path, hasPath := strings.Cut(s, "/")
	if hasPath {
		path = "/" + path
		if strings.HasSuffix(path, "*") {
			p.prefix, path = true, strings.TrimSuffix(path, "*")
		}
		if strings.Contains(path, "*") {
			return p, false
		}
		p.path = path
	}
	if h, ok := strings.CutPrefix(hostport, "*."); ok {
		p.subdomain, hostport = true, h
	}
	host, port, ok := splitHostPort(hostport)
	if !ok || host == "" || strings.Contains(host, "*") || strings.ContainsAny(host, "?#@ ") {
		return p, false
	}
	if port != "" && !allDigits(port) {
		return p, false
	}
	p.host, p.port = strings.ToLower(host), port
	return p, true
}

// ValidReferrerPattern reports whether s is a website restriction Google would accept.
func ValidReferrerPattern(s string) bool {
	_, ok := parseReferrerPattern(s)
	return ok
}

// MatchReferrer reports whether the Referer of a request matches one of the allowed patterns.
// The query string and fragment of the referrer are ignored. A request without a Referer never
// matches, as at Google.
func MatchReferrer(allowed []string, referrer string) bool {
	u, err := url.Parse(strings.TrimSpace(referrer))
	if err != nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	path := u.EscapedPath()
	for _, a := range allowed {
		p, ok := parseReferrerPattern(a)
		if !ok {
			continue
		}
		if p.scheme != "" && p.scheme != strings.ToLower(u.Scheme) {
			continue
		}
		if p.port != "" && p.port != port {
			continue
		}
		if p.subdomain {
			if !strings.HasSuffix(host, "."+p.host) {
				continue
			}
		} else if !sameHost(host, p.host) {
			continue
		}
		switch {
		case p.path == "":
			ok = path == "" || path == "/"
		case p.prefix:
			ok = strings.HasPrefix(path, p.path)
		default:
			ok = path == p.path
		}
		if ok {
			return true
		}
	}
	return false
}

// sameHost compares a referrer's host with a pattern's, IP addresses by value.
func sameHost(host, pattern string) bool {
	pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "["), "]")
	if a, err := netip.ParseAddr(host); err == nil {
		b, err := netip.ParseAddr(pattern)
		return err == nil && a.Unmap() == b.Unmap()
	}
	return host == pattern
}

// splitHostPort splits "host", "host:port", "[v6]" and "[v6]:port".
func splitHostPort(s string) (host, port string, ok bool) {
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", false
		}
		host, rest := s[:end+1], s[end+1:]
		if rest == "" {
			return host, "", true
		}
		if port, ok := strings.CutPrefix(rest, ":"); ok && port != "" {
			return host, port, true
		}
		return "", "", false
	}
	if strings.Count(s, ":") > 1 {
		return "", "", false // a bare IPv6 address needs brackets, as in a URL
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		return h, p, p != ""
	}
	return s, "", true
}

func validScheme(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || !(c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.')) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
