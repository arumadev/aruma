// Package ids generates and validates the identifiers that end up inside developers'
// configuration files. Their formats are fixed by ADR-007; the schema enforces the same rules.
package ids

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// APIKeyPrefix replaces Google's "AIza" (U-17). SDKs require "A" + 38 of [A-Za-z0-9_-].
const APIKeyPrefix = "Aru"

// Platform is an app platform as it appears inside app ids.
type Platform string

const (
	Web     Platform = "web"
	IOS     Platform = "ios"
	Android Platform = "android"
)

var (
	projectIDRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	saNameRe      = projectIDRe
	appIDRe       = regexp.MustCompile(`^1:([0-9]{12}):(web|ios|android):[0-9a-f]{16}$`)
	apiKeyRe      = regexp.MustCompile(`^A[A-Za-z0-9_-]{38}$`)
	packageNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
	bundleIDRe    = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)
	hexRe         = regexp.MustCompile(`^[0-9a-f]+$`)
)

// ProjectNumber returns a random 12-digit number that does not start with 0.
func ProjectNumber() int64 { return randomDigits(12) }

// ServiceAccountUniqueID returns a random 21-digit id that does not start with 0.
func ServiceAccountUniqueID() string {
	n, _ := rand.Int(rand.Reader, new(big.Int).Mul(big.NewInt(9), new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)))
	return n.Add(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)).String()
}

func randomDigits(n int) int64 {
	lo := int64(1)
	for i := 1; i < n; i++ {
		lo *= 10
	}
	r, _ := rand.Int(rand.Reader, big.NewInt(9*lo))
	return lo + r.Int64()
}

// AppID returns "1:<project_number>:<platform>:<16 hex>".
func AppID(projectNumber int64, p Platform) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("1:%d:%s:%s", projectNumber, p, hex.EncodeToString(b))
}

// APIKey returns a 39-character key: the private prefix and 36 random base64url characters.
func APIKey() string {
	b := make([]byte, 27)
	_, _ = rand.Read(b)
	return APIKeyPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// KeyID returns a 40-character hex id for a service account key.
func KeyID() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ETag returns a fresh entity tag.
func ETag() string { return "1_" + uuid.NewString() }

// ValidProjectID reports whether s is a valid project id: 6 to 30 of [a-z0-9-], starting with a
// letter and not ending with a hyphen.
func ValidProjectID(s string) bool { return projectIDRe.MatchString(s) }

// ValidServiceAccountName applies the project id rules to service account names.
func ValidServiceAccountName(s string) bool { return saNameRe.MatchString(s) }

// ValidAppID reports whether s has the app id format.
func ValidAppID(s string) bool { return appIDRe.MatchString(s) }

// AppIDProjectNumber extracts the project number embedded in an app id.
func AppIDProjectNumber(s string) (int64, bool) {
	m := appIDRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	var n int64
	_, err := fmt.Sscan(m[1], &n)
	return n, err == nil
}

// ValidAPIKey reports whether s satisfies the client SDKs' key check.
func ValidAPIKey(s string) bool { return apiKeyRe.MatchString(s) }

// ValidPackageName reports whether s is an Android application id.
func ValidPackageName(s string) bool { return len(s) <= 255 && packageNameRe.MatchString(s) }

// ValidBundleID reports whether s is an Apple bundle id.
func ValidBundleID(s string) bool { return len(s) <= 255 && bundleIDRe.MatchString(s) }

// SuggestProjectID derives a project id from a display name. Names without Latin letters (an
// Arabic name, say) fall back to "project"; a random suffix makes short ids long enough and
// lets callers retry when the suggestion is taken.
func SuggestProjectID(displayName string, suffix bool) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(displayName) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	id := strings.Trim(b.String(), "-")
	for id != "" && (id[0] < 'a' || id[0] > 'z') {
		id = strings.TrimLeft(id[1:], "-")
	}
	if id == "" {
		id, suffix = "project", true // a bare "project" would collide every time
	}
	if len(id) > 24 {
		id = strings.TrimRight(id[:24], "-")
	}
	if suffix || len(id) < 6 {
		id += "-" + randomSuffix(5)
	}
	return id
}

func randomSuffix(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, n)
	for i := range out {
		r, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		out[i] = alphabet[r.Int64()]
	}
	return string(out)
}

// ShaCertType is the kind of an Android signing certificate fingerprint.
type ShaCertType string

const (
	SHA1   ShaCertType = "SHA_1"
	SHA256 ShaCertType = "SHA_256"
)

// NormalizeSHA accepts a fingerprint as hex with or without colons, in any case, and returns
// it as lower-case hex with its type, inferred from the length.
func NormalizeSHA(s string) (string, ShaCertType, bool) {
	h := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), ":", ""))
	if !hexRe.MatchString(h) {
		return "", "", false
	}
	switch len(h) {
	case 40:
		return h, SHA1, true
	case 64:
		return h, SHA256, true
	}
	return "", "", false
}
