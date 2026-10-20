package api

import (
	"io"
	"net/http"
	"strings"

	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/services/rtdb/core"
)

// The REST API comes in R5. Its rules document is here already: Firebase's tools and the
// SDKs' own test suites load rules through it before anything else.

// maxRulesBody caps a rules document sent over REST.
const maxRulesBody = 256 << 10

// serveRules answers GET and PUT /.settings/rules.json, as the emulator and firebase-tools
// use it: an admin credential in Authorization (Bearer), the document as JSON (comments
// allowed), {"status":"ok"} after a PUT, the document as stored after a GET.
func (a *API) serveRules(w http.ResponseWriter, r *http.Request, t core.Target) {
	h := r.Header.Get("Authorization")
	scheme, token, _ := strings.Cut(h, " ")
	if !strings.EqualFold(scheme, "Bearer") || !(strings.HasPrefix(token, authn.ServiceTokenPrefix) || strings.HasPrefix(token, authn.AccessTokenPrefix)) {
		writeError(w, http.StatusUnauthorized, "Unauthorized request.")
		return
	}
	if _, err := a.verifyAdmin(r.Context(), t, strings.TrimSpace(token)); err != nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized request.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		doc, err := a.svc.Rules(r.Context(), t)
		if err != nil {
			e := core.AsError(err)
			writeError(w, e.HTTP, e.Message)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, doc)
	case http.MethodPut:
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRulesBody))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "Rules are too large.")
			return
		}
		if err := a.svc.SetRules(r.Context(), t, string(b)); err != nil {
			e := core.AsError(err)
			writeError(w, e.HTTP, e.Message)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "Unsupported request type")
	}
}
