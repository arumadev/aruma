package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/ratelimit"
	"github.com/arumadev/aruma/internal/services/auth/core"
)

// The pages the web SDK opens for signInWithPopup and signInWithRedirect, at
// <emulator URL>/emulator/auth/{handler,iframe} after connectAuthEmulator and at
// https://<authDomain>/__/auth/{handler,iframe} otherwise (docs/research/phase2-idp.md §2).
//
// The handler page starts a sign-in (the SDK's query), and is the provider's callback: a GET
// with state and code from Google, a cross-site form POST from Apple. It hands the result to
// the SDK as an AuthEvent: for popups through the iframe the SDK opened on the app page, for
// redirects through sessionStorage, which that iframe reads when the app reloads. The pages
// are static; everything request-specific travels in a JSON data block, so a strict CSP with
// script hashes covers them, and the only third-party resource is Google's gapi script on the
// iframe page, which the SDK's iframe protocol requires (ADR-013's declared exception).

const pageHTML = `<!doctype html>
<html lang="ar" dir="rtl">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex"><title>Aruma</title><style>` + pageStyle + `</style></head>
<body><main id="msg"><p lang="ar"></p><p lang="en" dir="ltr"></p></main>
<script type="application/json" id="aruma-idp">%DATA%</script>
<script>` + handlerScript + `</script>
</body></html>
`

const pageStyle = `body{font-family:system-ui,sans-serif;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center;background:#fafafa;color:#222}main{max-width:28rem;padding:1.5rem;text-align:center}p[lang=en]{color:#555}`

// handlerScript runs on the handler page. It starts the sign-in (keeping the sessionId in this
// tab only), or builds the AuthEvent from the provider's answer and delivers it.
const handlerScript = `(function () {
  var d = JSON.parse(document.getElementById('aruma-idp').textContent);
  var text = {
    working: ['جارٍ تسجيل الدخول…', 'Signing you in…'],
    done: ['تم. يمكنك إغلاق هذه النافذة.', 'Done. You can close this window.'],
    lost: ['تعذّر الوصول إلى التطبيق. أغلق النافذة وحاول مجددًا.', 'The app could not be reached. Close this window and try again.'],
    expired: ['انتهت جلسة تسجيل الدخول أو استُخدمت. حاول مجددًا.', 'This sign-in session has expired or was already used. Please try again.'],
    domain: ['هذا العنوان غير مصرّح به لهذا المشروع.', 'This address is not authorized for this project.'],
    disabled: ['طريقة الدخول هذه غير مفعّلة لهذا المشروع.', 'This sign-in method is not enabled for this project.'],
    invalid: ['طلب تسجيل الدخول غير صالح.', 'The sign-in request is not valid.'],
    cancelled: ['أُلغي تسجيل الدخول.', 'Sign-in was cancelled.']
  };
  function show(k) {
    var t = text[k] || text.invalid;
    document.querySelector('p[lang=ar]').textContent = t[0];
    document.querySelector('p[lang=en]').textContent = t[1];
  }
  function key() { return d.apiKey + ':' + d.appName; }
  function authEvent(f) {
    var ev = { type: d.authType, eventId: d.eventId || null, urlResponse: null, sessionId: null, postBody: null, tenantId: null, error: null };
    for (var k in f) { ev[k] = f[k]; }
    return ev;
  }
  // relay posts the event to the SDK's iframe on the opener, which may still be loading.
  function relay(ev, shown, tries) {
    var sent = false, frames = window.opener ? window.opener.frames : [];
    for (var i = 0; i < frames.length; i++) {
      try {
        var f = frames[i];
        if (f.location.origin !== location.origin || !/\/auth\/iframe$/.test(f.location.pathname)) { continue; }
        var q = new URLSearchParams(f.location.search);
        if (q.get('apiKey') === d.apiKey && q.get('appName') === d.appName) {
          f.postMessage({ eventType: 'sendAuthEvent', data: { authEvent: ev, storageKey: key() } }, location.origin);
          sent = true;
        }
      } catch (e) { /* a frame of another origin, or not loaded yet */ }
    }
    if (sent) {
      show(shown);
    } else if (tries > 0 && window.opener) {
      setTimeout(function () { relay(ev, shown, tries - 1); }, 250);
    } else {
      show('lost');
    }
  }
  function deliver(ev, shown) {
    if (/popup/i.test(d.authType)) {
      relay(ev, shown, 40);
    } else if (d.redirectUrl) {
      sessionStorage.setItem('firebase:redirectEvent:' + key(), JSON.stringify(ev));
      location.replace(d.redirectUrl);
    } else {
      show(shown);
    }
  }
  show(d.mode === 'error' ? d.reason : 'working');
  if (d.mode === 'start') {
    sessionStorage.setItem('aruma:idp:' + d.state, d.sessionId);
    location.replace(d.authUri);
  } else if (d.mode === 'finish') {
    var sid = sessionStorage.getItem('aruma:idp:' + d.state);
    sessionStorage.removeItem('aruma:idp:' + d.state);
    if (!sid) {
      deliver(authEvent({ error: { code: 'auth/web-storage-unsupported' } }), 'expired');
    } else {
      deliver(authEvent({ urlResponse: d.postBody ? d.callbackUrl : location.href, sessionId: sid, postBody: d.postBody || null }), 'done');
    }
  } else if (d.errorCode && d.authType) {
    deliver(authEvent({ error: { code: d.errorCode } }), d.reason);
  }
})();`

const iframeHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="robots" content="noindex"><title>Aruma</title></head>
<body><script>` + iframeScript + `</script>
<script src="https://apis.google.com/js/api.js"></script>
</body></html>
`

// iframeScript runs in the hidden iframe the SDK opens on the app page through gapi. It
// answers the SDK's webStorageSupport probe, relays AuthEvents from Aruma's own handler page,
// and once connected sends the redirect result stored for this app, or "no event", first: the
// SDK's startup redirect check waits for that event. A popup result that arrives earlier waits
// in memory until then.
const iframeScript = `(function () {
  var q = new URLSearchParams(location.search);
  var key = q.get('apiKey') + ':' + q.get('appName');
  var storage = 'firebase:redirectEvent:' + key;
  var parent = null, early = [];
  function send(ev) {
    parent.send('authEvent', { type: 'authEvent', authEvent: ev }, function () {}, gapi.iframes.CROSS_ORIGIN_IFRAMES_FILTER);
  }
  window.addEventListener('message', function (e) {
    var m = e.data;
    if (e.origin !== location.origin || !m || m.eventType !== 'sendAuthEvent' || !m.data || m.data.storageKey !== key) { return; }
    if (parent) { send(m.data.authEvent); } else { early.push(m.data.authEvent); }
  });
  window.gapi_onload = function () {
    gapi.load('gapi.iframes', { callback: function () {
      parent = gapi.iframes.getContext().getParentIframe();
      parent.register('webStorageSupport', function () {
        return { status: 'ACK', webStorageSupport: true };
      }, gapi.iframes.CROSS_ORIGIN_IFRAMES_FILTER);
      var stored = null;
      try { stored = JSON.parse(sessionStorage.getItem(storage)); } catch (e) { stored = null; }
      sessionStorage.removeItem(storage);
      send(stored || { type: 'unknown', eventId: null, urlResponse: null, sessionId: null, postBody: null, tenantId: null, error: { code: 'auth/no-auth-event' } });
      for (var i = 0; i < early.length; i++) { send(early[i]); }
      early = [];
    } });
  };
})();`

func sha256Source(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

var (
	handlerCSP = "default-src 'none'; script-src " + sha256Source(handlerScript) + "; style-src " + sha256Source(pageStyle) +
		"; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	iframeScriptSrc = sha256Source(iframeScript) + " https://apis.google.com"
)

// pageHeaders sets what every Auth page carries; the entry point's defaults (no framing, an
// isolated opener) are relaxed only where a page needs it.
func pageHeaders(w http.ResponseWriter, csp string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", csp)
}

// pageData is the JSON block the handler script reads.
type pageData struct {
	Mode        string `json:"mode"` // start, finish, error
	Reason      string `json:"reason,omitempty"`
	ErrorCode   string `json:"errorCode,omitempty"`
	AuthType    string `json:"authType,omitempty"`
	EventID     string `json:"eventId,omitempty"`
	APIKey      string `json:"apiKey,omitempty"`
	AppName     string `json:"appName,omitempty"`
	RedirectURL string `json:"redirectUrl,omitempty"`
	AuthURI     string `json:"authUri,omitempty"`
	State       string `json:"state,omitempty"`
	SessionID   string `json:"sessionId,omitempty"`
	CallbackURL string `json:"callbackUrl,omitempty"`
	PostBody    string `json:"postBody,omitempty"`
}

func writePage(w http.ResponseWriter, status int, d pageData) {
	pageHeaders(w, handlerCSP)
	// The popup must keep its opener, which is the app on another origin.
	w.Header().Set("Cross-Origin-Opener-Policy", "unsafe-none")
	b, _ := json.Marshal(d) // escapes <, > and &, so the data cannot close its script element
	w.WriteHeader(status)
	_, _ = io.WriteString(w, strings.Replace(pageHTML, "%DATA%", string(b), 1))
}

var authTypes = map[string]bool{
	"signInViaPopup": true, "signInViaRedirect": true, "linkViaPopup": true,
	"linkViaRedirect": true, "reauthViaPopup": true, "reauthViaRedirect": true,
}

// handlerPage serves /__/auth/handler and /emulator/auth/handler.
func (a *API) handlerPage(w http.ResponseWriter, r *http.Request) {
	if ok, _ := a.ipLimit.Allow(ratelimit.AddrKey(httpx.ClientAddr(r.Context()))); !ok {
		writePage(w, http.StatusTooManyRequests, pageData{Mode: "error", Reason: "invalid"})
		return
	}
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		form, perr := url.ParseQuery(string(body))
		if err != nil || perr != nil {
			writePage(w, http.StatusBadRequest, pageData{Mode: "error", Reason: "invalid"})
			return
		}
		a.finishPage(w, r, form, string(body))
		return
	}
	q := r.URL.Query()
	if q.Get("apiKey") == "" && q.Get("state") != "" {
		a.finishPage(w, r, q, "")
		return
	}
	a.startPage(w, r, q)
}

// startPage checks the SDK's request and sends the browser to the provider.
func (a *API) startPage(w http.ResponseWriter, r *http.Request, q url.Values) {
	d := pageData{Mode: "error", Reason: "invalid", AuthType: q.Get("authType"), EventID: q.Get("eventId"),
		APIKey: q.Get("apiKey"), AppName: q.Get("appName")}
	if !authTypes[d.AuthType] || d.AppName == "" || d.APIKey == "" || q.Get("tid") != "" {
		d.AuthType = "" // nothing trustworthy to deliver an error to
		writePage(w, http.StatusBadRequest, d)
		return
	}
	d.ErrorCode = "auth/argument-error"
	p, err := a.svc.ProjectForKey(r.Context(), d.APIKey, a.pageCaller(r))
	if err != nil {
		d.AuthType = ""
		status := http.StatusBadRequest
		if keyRefused(err) {
			status = http.StatusForbidden
		}
		writePage(w, status, d)
		return
	}
	redirectURL := q.Get("redirectUrl")
	if ok, err := a.svc.AuthorizedURL(r.Context(), p, redirectURL); err != nil || !ok {
		// Never deliver anything to an unauthorized page, not even an error.
		d.Reason, d.ErrorCode = "domain", "auth/unauthorized-domain"
		writePage(w, http.StatusForbidden, d)
		return
	}
	in := core.StartInput{
		ProviderID: q.Get("providerId"), CallbackURL: a.pageOrigin(r) + "/__/auth/handler", AuthType: d.AuthType,
		AppName: d.AppName, APIKey: d.APIKey, EventID: d.EventID,
		Scopes: strings.FieldsFunc(q.Get("scopes"), func(c rune) bool { return c == ',' }), RedirectURL: redirectURL,
	}
	if cp := q.Get("customParameters"); cp != "" {
		_ = json.Unmarshal([]byte(cp), &in.CustomParameters)
	}
	d.RedirectURL = redirectURL
	started, err := a.svc.StartIdP(r.Context(), p, in)
	if err != nil {
		var e *core.Error
		switch {
		case errors.Is(err, core.ErrOperationNotAllowed):
			d.Reason, d.ErrorCode = "disabled", "auth/operation-not-allowed"
		case errors.Is(err, core.ErrUnauthorizedDomain):
			d.Reason, d.ErrorCode, d.RedirectURL = "domain", "auth/unauthorized-domain", ""
		case errors.As(err, &e):
			d.ErrorCode = "auth/invalid-provider-id"
		default:
			a.log.Error("idp start", "error", err)
			d.ErrorCode = "auth/internal-error"
		}
		writePage(w, http.StatusBadRequest, d)
		return
	}
	writePage(w, http.StatusOK, pageData{Mode: "start", AuthURI: started.AuthURI, State: started.State, SessionID: started.SessionID})
}

// finishPage turns the provider's callback into an AuthEvent for the tab that started it.
// The session is only looked up here; signInWithIdp redeems it.
func (a *API) finishPage(w http.ResponseWriter, r *http.Request, params url.Values, postBody string) {
	pending, err := a.svc.PendingIdP(r.Context(), params.Get("state"))
	if err != nil {
		writePage(w, http.StatusBadRequest, pageData{Mode: "error", Reason: "expired"})
		return
	}
	d := pageData{
		AuthType: pending.AuthType, EventID: pending.EventID, APIKey: pending.APIKey, AppName: pending.AppName,
		RedirectURL: pending.RedirectURL,
	}
	switch e := params.Get("error"); {
	case e == "access_denied" || e == "user_cancelled_authorize":
		d.Mode, d.Reason, d.ErrorCode = "error", "cancelled", "auth/user-cancelled"
	case e != "":
		d.Mode, d.Reason, d.ErrorCode = "error", "invalid", "auth/internal-error"
	default:
		d.Mode, d.State, d.CallbackURL, d.PostBody = "finish", params.Get("state"), pending.CallbackURL, postBody
	}
	writePage(w, http.StatusOK, d)
}

// pageOrigin is the origin the handler was served from: Aruma's public URL, or https on
// another host (a project's own authDomain), which StartIdP then checks is authorized.
func (a *API) pageOrigin(r *http.Request) string {
	pub := a.svc.PublicURL()
	if u, err := url.Parse(pub); err == nil && strings.EqualFold(u.Host, r.Host) {
		return pub
	}
	return "https://" + r.Host
}

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// iframePage serves the relay iframe. Only the app's authorized domains may frame it: the
// iframe hands sign-in results to whoever embeds it.
func (a *API) iframePage(w http.ResponseWriter, r *http.Request) {
	ancestors := "'none'"
	if key := r.URL.Query().Get("apiKey"); key != "" {
		if p, err := a.svc.ProjectForKey(r.Context(), key, a.pageCaller(r)); err == nil {
			if domains, err := a.svc.AuthorizedDomains(r.Context(), p); err == nil {
				var srcs []string
				for _, d := range domains {
					if hostRe.MatchString(d) {
						srcs = append(srcs, "http://"+d+":*", "https://"+d+":*")
					}
				}
				if len(srcs) > 0 {
					ancestors = strings.Join(srcs, " ")
				}
			}
		}
	}
	pageHeaders(w, "default-src 'none'; script-src "+iframeScriptSrc+"; base-uri 'none'; form-action 'none'; frame-ancestors "+ancestors)
	h := w.Header()
	h.Del("X-Frame-Options") // frame-ancestors decides
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	_, _ = io.WriteString(w, iframeHTML)
}

// hostOnly strips the port from a Host header.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
