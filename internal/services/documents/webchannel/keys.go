package webchannel

import (
	"net/http"
	"net/netip"

	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// service is the API a key's API restrictions name for the Document Database.
const service = "firestore.googleapis.com"

// checkKey applies a handshake's API key as Auth applies one (ADR-027 §5.3): no key is accepted,
// as production accepts none from the mobile clients; a key must resolve to an active project,
// pass its restrictions for this caller, and be of the project the handshake's database names,
// or the handshake is refused before any session exists. It answers the refusal itself.
func (h *Handler) checkKey(w http.ResponseWriter, r *http.Request, key, project string, client netip.Addr) bool {
	if key == "" || h.keys == nil {
		return true
	}
	kp, err := h.keys.ProjectForKey(r.Context(), key)
	if err != nil {
		fail(w, http.StatusBadRequest, "API key not valid. Please pass a valid API key.")
		return false
	}
	caller := tcore.KeyCaller{
		Service:        service,
		Referrer:       r.Header.Get("Referer"),
		IP:             client,
		AndroidPackage: r.Header.Get("X-Android-Package"),
		AndroidCert:    r.Header.Get("X-Android-Cert"),
		IOSBundleID:    r.Header.Get("X-Ios-Bundle-Identifier"),
	}
	if blocked := kp.Key.Restrictions.Check(caller); blocked != nil {
		fail(w, http.StatusForbidden, blocked.Message)
		return false
	}
	if kp.ID != project {
		fail(w, http.StatusForbidden, "Missing or insufficient permissions.")
		return false
	}
	return true
}
