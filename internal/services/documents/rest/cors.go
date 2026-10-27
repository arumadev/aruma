package rest

import "net/http"

// The data and channel paths answer browsers as production does (ADR-027 §5.2; corpus
// f6-cors.json, f6-webchannel-*.json). The full web SDK sends its requests with credentials
// (webchannel_connection.ts; WebChannelBase.createXhrIo), which a browser refuses an answer of
// "*" to, so the request's Origin is echoed with Allow-Credentials, any origin, "null" included.
// That is safe on these paths alone: the identity is in the Authorization header, the body or
// the API key, and no request of them reads a cookie, so credentials give a foreign page nothing
// a script elsewhere could not send.
//
// A preflight is answered 200 with an empty text/html body; the headers it asks for are echoed.
// Production refuses some it does not know (x-made-up-header, 403), from a list it does not
// publish: every one is echoed here (parity-exceptions.json cors-unknown-request-header). An
// OPTIONS without Origin is production's empty 400.
const (
	// dataMethods and channelMethods are the methods a preflight allows.
	dataMethods    = "DELETE,GET,HEAD,OPTIONS,PATCH,POST,PUT"
	channelMethods = "GET,POST"
	// dataExpose is what production's data paths expose to a script: the headers its answer
	// carries, as recorded.
	dataExpose = "x-debug-tracking-id,vary,vary,vary,content-encoding,date,server,content-length"
)

// dataVary is the Vary of the data paths' answers, three lines as production's.
var dataVary = []string{"Origin", "X-Origin", "Referer"}

// cors answers a data path's (channel false) or a channel path's request for browsers, and a
// preflight itself, reporting so.
func cors(w http.ResponseWriter, r *http.Request, channel bool) (answered bool) {
	h := w.Header()
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	origin := r.Header.Get("Origin")
	if r.Method != http.MethodOptions {
		if channel {
			h.Set("Vary", "origin")
		} else {
			h["Vary"] = dataVary
		}
		if origin != "" {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			if !channel {
				h.Set("Access-Control-Expose-Headers", dataExpose)
			}
		}
		return false
	}
	if origin == "" {
		h["Vary"] = dataVary
		h.Set("Content-Type", "application/json; charset=UTF-8")
		h.Set("Content-Length", "0")
		w.WriteHeader(http.StatusBadRequest)
		return true
	}
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	if asked := r.Header.Get("Access-Control-Request-Headers"); asked != "" {
		h.Set("Access-Control-Allow-Headers", asked)
	}
	if channel {
		h.Set("Access-Control-Allow-Methods", channelMethods)
		h.Set("Vary", "origin")
	} else {
		h.Set("Access-Control-Allow-Methods", dataMethods)
		h.Set("Access-Control-Max-Age", "3600")
		h.Set("Vary", "origin, referer, x-origin")
	}
	h.Set("Content-Type", "text/html")
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
	return true
}
