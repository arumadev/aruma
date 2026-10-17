package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/identity/tokens"
	"github.com/osama300/aurma-project/internal/platform/httpx"
)

// discoveryRoutes publish each issuer's OpenID configuration and keys (ADR-004), so any OIDC
// library can verify Aruma tokens. Keys are published before they sign, so a few minutes of
// caching is safe.
func (a *API) discoveryRoutes(r *httpx.Router) {
	for _, d := range []struct {
		prefix  string
		purpose tokens.Purpose
		issuer  func(core.Project) string
	}{
		{"/issuers", tokens.IDToken, a.svc.Issuer},
		{"/session-issuers", tokens.SessionCookie, a.svc.SessionIssuer},
	} {
		handle(r, http.MethodGet, d.prefix+"/{project}/.well-known/openid-configuration", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := a.svc.ProjectByID(r.Context(), r.PathValue("project"))
			if err != nil {
				a.writeError(w, r, err)
				return
			}
			iss := d.issuer(p)
			writeCached(w, 3600, map[string]any{
				"issuer": iss, "jwks_uri": iss + "/.well-known/jwks.json",
				"response_types_supported": []string{"id_token"}, "subject_types_supported": []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		}))
		handle(r, http.MethodGet, d.prefix+"/{project}/.well-known/jwks.json", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := a.svc.ProjectByID(r.Context(), r.PathValue("project"))
			if err != nil {
				a.writeError(w, r, err)
				return
			}
			keys, err := a.svc.Tokens().JWKS(r.Context(), p.Number, d.purpose)
			if err != nil {
				a.writeError(w, r, err)
				return
			}
			writeCached(w, 300, map[string]any{"keys": keys})
		}))
	}
}

// writeCached writes a public, cacheable JSON response.
func writeCached(w http.ResponseWriter, maxAge int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(maxAge))
	_ = json.NewEncoder(w).Encode(v)
}
