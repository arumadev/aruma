package apitest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/platform/rules"
)

// fakeSignature is the signature segment of the tokens IDToken and UserToken make, which the
// environment's verifier accepts.
const fakeSignature = "c2ln"

// idTokens verifies the tokens IDToken and UserToken make, as Auth's verifier would answer them:
// of Project, uid their sub, every claim in Token; another signature, or another project, is
// invalid, and a token past its exp expired.
type idTokens struct{ project int64 }

func (v idTokens) Verify(_ context.Context, project int64, token string) (*idtoken.Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[2] != fakeSignature || project != v.project {
		return nil, idtoken.ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, idtoken.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var claims map[string]any
	if err := d.Decode(&claims); err != nil {
		return nil, idtoken.ErrInvalid
	}
	sub, _ := claims["sub"].(string)
	exp, _ := claims["exp"].(json.Number).Int64()
	if time.Now().Unix() >= exp {
		return nil, idtoken.ErrExpired
	}
	return &idtoken.Claims{UID: sub, Expires: time.Unix(exp, 0), Token: claims}, nil
}

// UserToken is an end user's ID token of Project for uid, with claims beside the standard ones,
// which the environment's verifier accepts.
func UserToken(t testing.TB, uid string, claims map[string]any) string {
	t.Helper()
	now := time.Now().Unix()
	c := map[string]any{"iss": PublicURL + "/issuers/" + Project, "aud": Project, "sub": uid, "user_id": uid,
		"iat": now, "exp": now + 3600, "firebase": map[string]any{"sign_in_provider": "password"}}
	maps.Copy(c, claims)
	return segment(t, map[string]any{"alg": "RS256", "typ": "JWT"}) + "." + segment(t, c) + "." + fakeSignature
}

// PublishFirestoreRules publishes src as Project's cloud.firestore release.
func (e *Env) PublishFirestoreRules(t testing.TB, src string) {
	t.Helper()
	rs, issues, err := e.Tenancy.CreateRuleset(t.Context(), e.Owner, Project, rules.Firestore,
		[]rules.File{{Name: "firestore.rules", Content: src}})
	if err != nil || issues.Err() != nil {
		t.Fatalf("CreateRuleset: %v %v", err, issues.Err())
	}
	if _, err := e.Tenancy.PublishRelease(t.Context(), e.Owner, Project, "cloud.firestore", rs.Name); err != nil {
		t.Fatalf("PublishRelease: %v", err)
	}
}
