package rest

import "net/http"

// allowedHeaders are the request headers a browser may send to the data paths: ADR-014's, with
// the ones the Firestore SDKs send (ADR-027 §5.2).
const allowedHeaders = "authorization, content-type, google-cloud-resource-prefix, x-client-version, x-firebase-appcheck, " +
	"x-firebase-client, x-firebase-gmpid, x-firebase-locale, x-goog-api-client, x-goog-api-key, x-goog-request-params"

// credentialed is the one switch of ADR-027 §5.2: whether the data and channel paths answer a
// browser's credentialed request. The full web SDK sends its requests with credentials: its XHRs
// set withCredentials, the RPCs' (webchannel_connection.ts performRPCRequest) and the WebChannel's
// (supportsCrossDomainXhr, WebChannelBase.createXhrIo), and a browser refuses "*" to such a
// request. So the request's Origin is echoed with Allow-Credentials. That is safe on these paths
// alone: the identity is in the Authorization header, the body or the API key, and no request of
// them reads a cookie, so credentials give a foreign page nothing a script elsewhere could not
// send. A production recording settles it (ADR-027 §5.5); false is ADR-014's "*" without
// credentials.
const credentialed = true

// cors opens a data or channel path to every origin (credentialed), and answers a preflight
// itself, reporting so.
func cors(w http.ResponseWriter, r *http.Request) (answered bool) {
	h := w.Header()
	if origin := r.Header.Get("Origin"); credentialed && origin != "" {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
	} else {
		h.Set("Access-Control-Allow-Origin", "*")
	}
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
