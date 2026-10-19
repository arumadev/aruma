package console

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/services/auth/core"
)

// authProviderRoutes registers the Google and Apple sign-in settings routes. Secrets are
// write-only: responses say whether one is set, never what it is.
func (h *handlers) authProviderRoutes() {
	h.m.Handle(anyCred, "GET", "/console/v1/projects/{project}/auth/providers", h.listAuthProviders)
	h.m.Handle(anyCred, "PATCH", "/console/v1/projects/{project}/auth/providers/{providerId}", h.updateAuthProvider)
}

func providerJSON(c core.IdPConfig) map[string]any {
	if c.ProviderID == core.AppleProvider {
		return map[string]any{
			"providerId": c.ProviderID, "enabled": c.Enabled, "servicesId": c.ClientID,
			"teamId": c.TeamID, "keyId": c.KeyID, "privateKeySet": c.PrivateKeySet,
		}
	}
	return map[string]any{"providerId": c.ProviderID, "enabled": c.Enabled, "clientId": c.ClientID, "clientSecretSet": c.ClientSecretSet}
}

// redirectURI is the URL to register with Google and Apple: the handler on the project's own
// authDomain, or on Aruma's public URL while the project uses the shared default.
func (h *handlers) redirectURI(r *http.Request, p core.Project) (string, error) {
	pub := h.svc.PublicURL()
	domain, err := h.svc.ProjectAuthDomain(r.Context(), p)
	if err != nil {
		return "", err
	}
	if u, err := url.Parse(pub); err == nil && domain != "" && !strings.EqualFold(u.Hostname(), domain) {
		return "https://" + domain + "/__/auth/handler", nil
	}
	return pub + "/__/auth/handler", nil
}

func (h *handlers) listAuthProviders(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	configs, err := h.svc.IdPConfigs(r.Context(), p)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	redirect, err := h.redirectURI(r, p)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(configs))
	for _, c := range configs {
		out = append(out, providerJSON(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"redirectUri": redirect, "providers": out})
}

func (h *handlers) updateAuthProvider(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
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
		h.m.Fail(w, r, err)
		return
	}
	provider := r.PathValue("providerId")
	u := core.IdPConfigUpdate{Enabled: in.Enabled, ClientSecret: in.ClientSecret, TeamID: in.TeamID, KeyID: in.KeyID, PrivateKey: in.PrivateKey}
	switch {
	case provider == core.AppleProvider && in.ClientID != nil:
		h.m.Fail(w, r, apierr.Invalid("clientId", "Apple uses servicesId."))
		return
	case provider == core.GoogleProvider && in.ServicesID != nil:
		h.m.Fail(w, r, apierr.Invalid("servicesId", "Google uses clientId."))
		return
	case provider == core.AppleProvider:
		u.ClientID = in.ServicesID
	default:
		u.ClientID = in.ClientID
	}
	c, err := h.svc.UpdateIdPConfig(r.Context(), p, provider, u)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, providerJSON(c))
}
