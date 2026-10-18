package console

import (
	"net/http"
	"net/url"
	"strings"

	identity "github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/httpx"
)

// authProviderRoutes registers the Google and Apple sign-in settings routes. Secrets are
// write-only: responses say whether one is set, never what it is.
func (a *API) authProviderRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {
	with(anyCred, "GET", "/console/v1/projects/{project}/auth/providers", a.listAuthProviders)
	with(anyCred, "PATCH", "/console/v1/projects/{project}/auth/providers/{providerId}", a.updateAuthProvider)
}

func providerJSON(c identity.IdPConfig) map[string]any {
	if c.ProviderID == identity.AppleProvider {
		return map[string]any{
			"providerId": c.ProviderID, "enabled": c.Enabled, "servicesId": c.ClientID,
			"teamId": c.TeamID, "keyId": c.KeyID, "privateKeySet": c.PrivateKeySet,
		}
	}
	return map[string]any{"providerId": c.ProviderID, "enabled": c.Enabled, "clientId": c.ClientID, "clientSecretSet": c.ClientSecretSet}
}

// redirectURI is the URL to register with Google and Apple: the handler on the project's own
// authDomain, or on Aruma's public URL while the project uses the shared default.
func (a *API) redirectURI(r *http.Request, p identity.Project) (string, error) {
	pub := a.opts.Auth.PublicURL()
	domain, err := a.opts.Auth.ProjectAuthDomain(r.Context(), p)
	if err != nil {
		return "", err
	}
	if u, err := url.Parse(pub); err == nil && domain != "" && !strings.EqualFold(u.Hostname(), domain) {
		return "https://" + domain + "/__/auth/handler", nil
	}
	return pub + "/__/auth/handler", nil
}

func (a *API) listAuthProviders(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	configs, err := a.opts.Auth.IdPConfigs(r.Context(), p)
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	redirect, err := a.redirectURI(r, p)
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(configs))
	for _, c := range configs {
		out = append(out, providerJSON(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"redirectUri": redirect, "providers": out})
}

func (a *API) updateAuthProvider(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Enabled      *bool   `json:"enabled"`
		ClientID     *string `json:"clientId"`
		ClientSecret *string `json:"clientSecret"`
		ServicesID   *string `json:"servicesId"`
		TeamID       *string `json:"teamId"`
		KeyID        *string `json:"keyId"`
		PrivateKey   *string `json:"privateKey"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	provider := r.PathValue("providerId")
	u := identity.IdPConfigUpdate{Enabled: in.Enabled, ClientSecret: in.ClientSecret, TeamID: in.TeamID, KeyID: in.KeyID, PrivateKey: in.PrivateKey}
	switch {
	case provider == identity.AppleProvider && in.ClientID != nil:
		a.fail(w, r, apierr.Invalid("clientId", "Apple uses servicesId."))
		return
	case provider == identity.GoogleProvider && in.ServicesID != nil:
		a.fail(w, r, apierr.Invalid("servicesId", "Google uses clientId."))
		return
	case provider == identity.AppleProvider:
		u.ClientID = in.ServicesID
	default:
		u.ClientID = in.ClientID
	}
	c, err := a.opts.Auth.UpdateIdPConfig(r.Context(), p, provider, u)
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, providerJSON(c))
}
