package rest

import "net/http"

// allowedHeaders are the request headers a browser may send to the data paths: ADR-014's, with
// the ones the Firestore SDKs send (ADR-027 §5.2).
const allowedHeaders = "authorization, content-type, google-cloud-resource-prefix, x-client-version, x-firebase-appcheck, " +
	"x-firebase-client, x-firebase-gmpid, x-firebase-locale, x-goog-api-client, x-goog-api-key, x-goog-request-params"

// cors opens a data path to every origin, as ADR-014 opens Auth's client paths: the identity is
// in the Authorization header, the body or the API key, never a cookie, so credentials are not
// allowed (ADR-027 §5.2; whether the full web SDK's XHR needs them is the F1 browser test's to
// settle). It answers a preflight itself and reports so.
func cors(w http.ResponseWriter, r *http.Request) (answered bool) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Expose-Headers", "X-HTTP-Session-Id")
	if r.Method != http.MethodOptions {
		return false
	}
	h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE")
	h.Set("Access-Control-Allow-Headers", allowedHeaders)
	h.Set("Access-Control-Max-Age", "3600")
	w.WriteHeader(http.StatusNoContent)
	return true
}
