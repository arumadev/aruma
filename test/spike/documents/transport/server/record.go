package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// recorded are the headers whose values are written down as sent. Every other header is recorded
// by name and length only, and authorization by its shape (credentialShape): no token, key or
// signature ever reaches the record.
var recorded = []string{
	"content-type", "te", "user-agent", "x-goog-api-client", "google-cloud-resource-prefix",
	"x-goog-request-params", "grpc-timeout", "grpc-accept-encoding", "grpc-encoding",
	"x-goog-is-transaction", "x-firebase-gmpid", "x-firebase-client-log-type", "accept",
	"accept-encoding", "x-goog-user-project", "x-server-timeout", "connection", "upgrade",
}

// entry is one line of the record (JSON Lines).
type entry struct {
	Time          string            `json:"time"`
	Listener      string            `json:"listener"` // e.g. "h2c-connect"
	Proto         string            `json:"proto"`
	Method        string            `json:"method"`
	Path          string            `json:"path"`
	Headers       map[string]string `json:"headers"`
	Authorization string            `json:"authorization,omitempty"`
	Other         []string          `json:"other_headers,omitempty"` // name=<n bytes>
	Grant         string            `json:"grant,omitempty"`         // the token endpoint's form, by shape
	Blocked       string            `json:"blocked,omitempty"`       // the guard proxy's refusals
}

// recorder writes the record and knows the fake service account, to check its JWTs.
type recorder struct {
	mu     sync.Mutex
	out    *os.File
	log    *slog.Logger
	sa     *fakeSA
	issued sync.Map // access tokens from the token endpoint
}

func (rc *recorder) write(e entry) {
	e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	line, _ := json.Marshal(e)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.out != nil {
		rc.out.Write(append(line, '\n'))
	}
}

// request records what a client sent, before any handler sees it.
func (rc *recorder) request(listener string, r *http.Request) {
	e := entry{Listener: listener, Proto: r.Proto, Method: r.Method, Path: r.URL.Path, Headers: map[string]string{}}
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		v := strings.Join(values, ", ")
		switch {
		case lower == "authorization":
			e.Authorization = rc.credentialShape(v)
		case slices.Contains(recorded, lower):
			e.Headers[lower] = v
		default:
			e.Other = append(e.Other, fmt.Sprintf("%s=<%d bytes>", lower, len(v)))
		}
	}
	slices.Sort(e.Other)
	rc.write(e)
	rc.log.Info("request", "listener", listener, "proto", r.Proto, "path", r.URL.Path,
		"content_type", r.Header.Get("Content-Type"), "authorization", e.Authorization)
}

// credentialShape describes an Authorization header without its secret: the scheme, and for a
// JWT its header and claims with the fake account's own values named instead of shown, the
// token's lifetime, and whether the fake key's signature verifies.
func (rc *recorder) credentialShape(v string) string {
	scheme, token, _ := strings.Cut(v, " ")
	switch {
	case token == "":
		return fmt.Sprintf("%s <empty>", scheme)
	case token == "owner":
		return scheme + " owner"
	case strings.Count(token, ".") == 2:
		return scheme + " JWT " + rc.jwtShape(token)
	}
	if _, ok := rc.issued.Load(token); ok {
		return scheme + " <access token issued by this server's token endpoint>"
	}
	return fmt.Sprintf("%s <opaque, %d chars, prefix %q>", scheme, len(token), prefix(token))
}

func prefix(s string) string {
	if i := strings.IndexAny(s, "._-"); i > 0 && i < 12 {
		return s[:i+1]
	}
	return s[:min(len(s), 4)]
}

// jwtShape renders a JWT as {header} {claims} signature=…, e.g.
// {alg=RS256 kid=<private_key_id> typ=JWT} {aud=… exp=iat+3600s iat=now iss=<client_email> …}.
func (rc *recorder) jwtShape(token string) string {
	parts := strings.Split(token, ".")
	var head, claims map[string]any
	if decodeJSON(parts[0], &head) != nil || decodeJSON(parts[1], &claims) != nil {
		return "<undecodable>"
	}
	var b strings.Builder
	b.WriteString("{")
	for i, k := range sortedKeys(head) {
		if i > 0 {
			b.WriteString(" ")
		}
		v := fmt.Sprint(head[k])
		if k == "kid" && rc.sa != nil && v == rc.sa.KeyID {
			v = "<private_key_id>"
		}
		fmt.Fprintf(&b, "%s=%s", k, v)
	}
	b.WriteString("} {")
	iat, _ := claims["iat"].(float64)
	for i, k := range sortedKeys(claims) {
		if i > 0 {
			b.WriteString(" ")
		}
		v := fmt.Sprint(claims[k])
		switch {
		case rc.sa != nil && v == rc.sa.ClientEmail:
			v = "<client_email>"
		case k == "iat":
			v = fmt.Sprintf("now%+ds", int64(iat)-time.Now().Unix())
		case k == "exp":
			exp, _ := claims[k].(float64)
			v = fmt.Sprintf("iat+%ds", int64(exp-iat))
		}
		fmt.Fprintf(&b, "%s=%s", k, v)
	}
	b.WriteString("} signature=")
	b.WriteString(rc.verify(parts))
	return b.String()
}

// verify reports whether an RS256 signature verifies with the fake service account's key.
func (rc *recorder) verify(parts []string) string {
	if rc.sa == nil {
		return "unchecked"
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "undecodable"
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&rc.sa.key.PublicKey, crypto.SHA256, digest[:], sig) != nil {
		return "INVALID"
	}
	return "valid(fake key)"
}

func decodeJSON(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func sortedKeys(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }

// tokenEndpoint is a stand-in for Aruma's /oauth2/v1/token (RFC 7523 JWT bearer grant). It
// records the grant by shape and answers with an opaque access token, which later requests are
// recognised by.
func (rc *recorder) tokenEndpoint(listener string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		grant := "grant_type=" + r.PostForm.Get("grant_type")
		if a := r.PostForm.Get("assertion"); a != "" {
			grant += " assertion=JWT " + rc.jwtShape(a)
		}
		for k := range r.PostForm {
			if k != "grant_type" && k != "assertion" {
				grant += " " + k + "=<present>"
			}
		}
		rc.write(entry{Listener: listener, Proto: r.Proto, Method: r.Method, Path: r.URL.Path, Grant: grant,
			Headers: map[string]string{"content-type": r.Header.Get("Content-Type"), "user-agent": r.Header.Get("User-Agent")}})
		rc.log.Info("token grant", "listener", listener, "grant", grant)
		token := "spike_at_" + randomHex(16)
		rc.issued.Store(token, true)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600})
	}
}

// guardProxy refuses every request a client sends through it (HTTPS_PROXY and HTTP_PROXY point
// here, NO_PROXY names loopback): whatever would have left the machine is recorded and refused.
func (rc *recorder) guardProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Host
		if r.Method != http.MethodConnect && r.URL.Host != "" {
			target = r.URL.Scheme + "://" + r.URL.Host + r.URL.Path
		}
		rc.write(entry{Listener: "guard-proxy", Proto: r.Proto, Method: r.Method, Blocked: target})
		rc.log.Warn("guard proxy refused", "method", r.Method, "target", target)
		http.Error(w, "refused by the spike's network guard", http.StatusForbidden)
	})
}

// fakeSA is a made-up service account: a local RSA key and an invented client_email. It is no
// credential for anything; the key is generated on first use and never leaves .generated.
type fakeSA struct {
	ClientEmail string
	KeyID       string
	key         *rsa.PrivateKey
}

const fakeEmail = "firestore-spike@spike-project.sa.aruma.test"

// loadOrMakeSA reads dir/key.pem, or makes it, and writes the two key files the clients load:
// sa-google.json with Google's token_uri, and sa-aruma.json whose token_uri is tokenURL (this
// server's endpoint, as in the key files Aruma issues).
func loadOrMakeSA(dir, tokenURL string) (*fakeSA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "key.pem")
	var key *rsa.PrivateKey
	if b, err := os.ReadFile(keyPath); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, errors.New("key.pem: no PEM block")
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key = k.(*rsa.PrivateKey)
	} else {
		if key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			return nil, err
		}
		der, _ := x509.MarshalPKCS8PrivateKey(key)
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	sum := sha256.Sum256(pub)
	sa := &fakeSA{ClientEmail: fakeEmail, KeyID: hex.EncodeToString(sum[:20]), key: key}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	for name, uri := range map[string]string{"sa-google.json": "https://oauth2.googleapis.com/token", "sa-aruma.json": tokenURL} {
		b, _ := json.MarshalIndent(map[string]string{
			"type": "service_account", "project_id": "spike-project", "private_key_id": sa.KeyID,
			"private_key": keyPEM, "client_email": sa.ClientEmail, "client_id": "100000000000000000001",
			"auth_uri": "https://accounts.google.com/o/oauth2/auth", "token_uri": uri,
		}, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return nil, err
		}
	}
	return sa, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
