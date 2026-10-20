package acceptance

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// API key restrictions, as Google applies them to the Identity Toolkit and Secure Token APIs.

// restrict replaces the restrictions of the project's key through the console.
func (p *authProject) restrict(restrictions map[string]any) {
	p.env.t.Helper()
	keys := p.owner.mustOK(p.owner.do("GET", "/console/v1/projects/"+p.id+"/apiKeys", nil)).list("keys")
	for _, k := range keys {
		if k["keyString"] == p.key {
			p.owner.mustOK(p.owner.do("PATCH", "/console/v1/projects/"+p.id+"/apiKeys/"+k["uid"].(string), map[string]any{"restrictions": restrictions}))
			return
		}
	}
	p.env.t.Fatal("the project's key is not listed")
}

// refreshWith exchanges a refresh token with extra request headers.
func (p *authProject) refreshWith(token string, headers map[string]string) *Response {
	p.env.t.Helper()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}}
	req, err := http.NewRequest("POST", p.env.URL+sts+"?key="+p.key, strings.NewReader(form.Encode()))
	if err != nil {
		p.env.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.env.t.Fatal(err)
	}
	return decodeResponse(p.env.t, resp)
}

// keyBlocked asserts Google's answer to a request a key's restrictions refuse: 403
// PERMISSION_DENIED, the message as the code the SDKs read, errors[].reason "forbidden" and a
// google.rpc.ErrorInfo detail naming the reason, the project and the service.
func keyBlocked(t *testing.T, p *authProject, r *Response, reason, service, message string) {
	t.Helper()
	if r.Status != 403 || r.errStatus() != "PERMISSION_DENIED" || r.str("error", "message") != message {
		t.Fatalf("want 403 %q, got %d: %s", message, r.Status, r.Body)
	}
	e := r.JSON["error"].(map[string]any)
	if e["code"] != float64(403) {
		t.Fatalf("code: %s", r.Body)
	}
	want := []any{map[string]any{"message": message, "domain": "global", "reason": "forbidden"}}
	if !reflect.DeepEqual(e["errors"], want) {
		t.Fatalf("errors: %s", r.Body)
	}
	details, _ := e["details"].([]any)
	if len(details) != 1 {
		t.Fatalf("details: %s", r.Body)
	}
	d := details[0].(map[string]any)
	md, _ := d["metadata"].(map[string]any)
	if d["@type"] != "type.googleapis.com/google.rpc.ErrorInfo" || d["reason"] != reason || d["domain"] != "googleapis.com" ||
		md["consumer"] != "projects/"+p.number || md["service"] != service {
		t.Fatalf("ErrorInfo: %s", r.Body)
	}
}

const (
	itkService = "identitytoolkit.googleapis.com"
	stsService = "securetoken.googleapis.com"
)

// The finding this closes: a key restricted to one website must refuse another, and a request
// without a Referer, on every client call and on the token refresh.
func TestAuthKeyWebsiteRestriction(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	p.restrict(map[string]any{"browserKeyRestrictions": map[string]any{"allowedReferrers": []string{"https://good.example/*"}}})

	good := map[string]string{"Referer": "https://good.example/app/login", "Origin": "https://good.example"}
	evil := map[string]string{"Referer": "https://evil.example/", "Origin": "https://evil.example"}
	body := map[string]any{"email": "new@example.com", "password": "secret1", "returnSecureToken": true}

	keyBlocked(t, p, p.callWith(itk+"accounts:signUp", body, evil), "API_KEY_HTTP_REFERRER_BLOCKED", itkService,
		"Requests from referer https://evil.example/ are blocked.")
	keyBlocked(t, p, p.call(itk+"accounts:signUp", body), "API_KEY_HTTP_REFERRER_BLOCKED", itkService,
		"Requests from referer <empty> are blocked.")
	// Origin alone is not a referrer, as at Google.
	keyBlocked(t, p, p.callWith(itk+"accounts:signUp", body, map[string]string{"Origin": "https://good.example"}),
		"API_KEY_HTTP_REFERRER_BLOCKED", itkService, "Requests from referer <empty> are blocked.")
	keyBlocked(t, p, p.callWith(rp+"verifyPassword", map[string]any{"email": "sara@example.com", "password": "secret1"}, evil),
		"API_KEY_HTTP_REFERRER_BLOCKED", itkService, "Requests from referer https://evil.example/ are blocked.")
	keyBlocked(t, p, p.refreshWith(up.str("refreshToken"), evil), "API_KEY_HTTP_REFERRER_BLOCKED", stsService,
		"Requests from referer https://evil.example/ are blocked.")
	keyBlocked(t, p, p.refresh(up.str("refreshToken")), "API_KEY_HTTP_REFERRER_BLOCKED", stsService,
		"Requests from referer <empty> are blocked.")
	cfg := e.client()
	cfg.header["Referer"] = "https://evil.example/"
	keyBlocked(t, p, cfg.do("GET", itk+"projects?key="+p.key, nil), "API_KEY_HTTP_REFERRER_BLOCKED", itkService,
		"Requests from referer https://evil.example/ are blocked.")
	if n := len(p.owner.mustOK(p.owner.do("GET", "/console/v1/projects/chat-app/auth/users", nil)).list("users")); n != 1 {
		t.Fatalf("a refused signUp created a user: %d users", n)
	}

	// The allowed site works throughout, the web SDK's origin-only referrer included.
	created := ok(t, p.callWith(itk+"accounts:signUp", body, good))
	if created.str("idToken") == "" {
		t.Fatalf("signUp: %s", created.Body)
	}
	ok(t, p.callWith(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1"},
		map[string]string{"Referer": "https://good.example/"}))
	ok(t, p.refreshWith(up.str("refreshToken"), good))
	cfg.header["Referer"] = "https://good.example/"
	ok(t, cfg.do("GET", itk+"projects?key="+p.key, nil))

	// Lifting the restriction restores today's behaviour.
	p.restrict(map[string]any{})
	ok(t, p.refresh(up.str("refreshToken")))
}

// API restrictions: the Identity Toolkit API covers the client calls, the Secure Token API the
// refresh; a key that lists neither, or only the first, is refused where Google refuses it.
func TestAuthKeyAPIRestriction(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	body := map[string]any{"email": "new@example.com", "password": "secret1"}

	// The reviewed case: a key limited to Firestore (and one website) cannot sign anyone up.
	p.restrict(map[string]any{
		"browserKeyRestrictions": map[string]any{"allowedReferrers": []string{"https://good.example/*"}},
		"apiTargets":             []map[string]any{{"service": "firestore.googleapis.com"}},
	})
	good := map[string]string{"Referer": "https://good.example/"}
	keyBlocked(t, p, p.callWith(itk+"accounts:signUp", body, good), "API_KEY_SERVICE_BLOCKED", itkService,
		"Requests to this API identitytoolkit.googleapis.com are blocked.")
	keyBlocked(t, p, p.callWith(itk+"accounts:signUp", body, map[string]string{"Referer": "https://evil.example/"}), "API_KEY_SERVICE_BLOCKED", itkService,
		"Requests to this API identitytoolkit.googleapis.com are blocked.")
	keyBlocked(t, p, p.callWith(rp+"signupNewUser", body, good), "API_KEY_SERVICE_BLOCKED", itkService,
		"Requests to this API identitytoolkit.googleapis.com are blocked.")

	// Identity Toolkit only: sign-in works, the refresh is the Secure Token API and is refused.
	p.restrict(map[string]any{"apiTargets": []map[string]any{{"service": itkService}}})
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1"}))
	keyBlocked(t, p, p.refresh(in.str("refreshToken")), "API_KEY_SERVICE_BLOCKED", stsService,
		"Requests to this API securetoken.googleapis.com are blocked.")

	// Both, as Firebase's own auto-created keys list them.
	p.restrict(map[string]any{"apiTargets": []map[string]any{{"service": itkService}, {"service": stsService}, {"service": "firestore.googleapis.com"}}})
	ok(t, p.refresh(up.str("refreshToken")))
	ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": up.str("idToken")}))
}

// IP address restrictions use the client address the server resolves, through trusted
// proxies only.
func TestAuthKeyIPRestriction(t *testing.T) {
	e := newEnv(t, map[string]string{"ARUMA_TRUSTED_PROXIES": "127.0.0.1"})
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	p.restrict(map[string]any{"serverKeyRestrictions": map[string]any{"allowedIps": []string{"198.51.100.0/24", "2001:db8::/64"}}})

	from := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }
	login := map[string]any{"email": "sara@example.com", "password": "secret1"}
	ok(t, p.callWith(itk+"accounts:signInWithPassword", login, from("198.51.100.20")))
	ok(t, p.callWith(itk+"accounts:signInWithPassword", login, from("2001:db8::7")))
	ok(t, p.refreshWith(up.str("refreshToken"), from("198.51.100.20")))
	keyBlocked(t, p, p.callWith(itk+"accounts:signInWithPassword", login, from("203.0.113.5")), "API_KEY_IP_ADDRESS_BLOCKED", itkService,
		"The provided API key has an IP address restriction. The originating IP address of the call (203.0.113.5) violates this restriction.")
	keyBlocked(t, p, p.refreshWith(up.str("refreshToken"), from("2001:db8:1::7")), "API_KEY_IP_ADDRESS_BLOCKED", stsService,
		"The provided API key has an IP address restriction. The originating IP address of the call (2001:db8:1::7) violates this restriction.")
	// Without a proxy in front, the peer address is the caller's.
	keyBlocked(t, p, p.call(itk+"accounts:signInWithPassword", login), "API_KEY_IP_ADDRESS_BLOCKED", itkService,
		"The provided API key has an IP address restriction. The originating IP address of the call (127.0.0.1) violates this restriction.")

	// A forwarded address from an untrusted peer is ignored: it cannot unlock the key.
	e2 := newEnv(t, nil)
	q := e2.authProject("chat-app")
	q.restrict(map[string]any{"serverKeyRestrictions": map[string]any{"allowedIps": []string{"198.51.100.0/24"}}})
	keyBlocked(t, q, q.callWith(itk+"accounts:signUp", login, from("198.51.100.20")), "API_KEY_IP_ADDRESS_BLOCKED", itkService,
		"The provided API key has an IP address restriction. The originating IP address of the call (127.0.0.1) violates this restriction.")
	q.restrict(map[string]any{"serverKeyRestrictions": map[string]any{"allowedIps": []string{"127.0.0.1"}}})
	ok(t, q.call(itk+"accounts:signUp", login))
}

const (
	androidSHA1   = "AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01"
	androidHeader = "ABCDEF0123456789ABCDEF0123456789ABCDEF01" // X-Android-Cert: upper-case hex, no colons
)

// Android restrictions read X-Android-Package and X-Android-Cert, which the Android SDKs send.
func TestAuthKeyAndroidRestriction(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	p.restrict(map[string]any{"androidKeyRestrictions": map[string]any{"allowedApplications": []map[string]any{
		{"packageName": "com.example.chat", "sha1Fingerprint": androidSHA1},
	}}})

	app := map[string]string{"X-Android-Package": "com.example.chat", "X-Android-Cert": androidHeader}
	login := map[string]any{"email": "sara@example.com", "password": "secret1"}
	ok(t, p.callWith(itk+"accounts:signInWithPassword", login, app))
	ok(t, p.refreshWith(up.str("refreshToken"), app))

	resigned := map[string]string{"X-Android-Package": "com.example.chat", "X-Android-Cert": "0000000000000000000000000000000000000000"}
	keyBlocked(t, p, p.callWith(itk+"accounts:signInWithPassword", login, resigned), "API_KEY_ANDROID_APP_BLOCKED", itkService,
		"Requests from this Android client application com.example.chat are blocked.")
	other := map[string]string{"X-Android-Package": "com.example.other", "X-Android-Cert": androidHeader}
	keyBlocked(t, p, p.refreshWith(up.str("refreshToken"), other), "API_KEY_ANDROID_APP_BLOCKED", stsService,
		"Requests from this Android client application com.example.other are blocked.")
	r := p.call(itk+"accounts:signInWithPassword", login)
	keyBlocked(t, p, r, "API_KEY_ANDROID_APP_BLOCKED", itkService, "Requests from this Android client application <empty> are blocked.")
	if md := r.JSON["error"].(map[string]any)["details"].([]any)[0].(map[string]any)["metadata"].(map[string]any); md["androidPackage"] != "<empty>" {
		t.Fatalf("androidPackage: %s", r.Body)
	}
}

// iOS restrictions read X-Ios-Bundle-Identifier, which the iOS SDK sends on every Auth call,
// the v3 paths and the refresh included.
func TestAuthKeyIOSRestriction(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	p.restrict(map[string]any{"iosKeyRestrictions": map[string]any{"allowedBundleIds": []string{"com.example.chat"}}})

	app := map[string]string{"X-Ios-Bundle-Identifier": "com.example.chat"}
	login := map[string]any{"email": "sara@example.com", "password": "secret1", "returnSecureToken": true}
	ok(t, p.callWith(rp+"verifyPassword", login, app))
	ok(t, p.refreshWith(up.str("refreshToken"), app))
	keyBlocked(t, p, p.callWith(rp+"verifyPassword", login, map[string]string{"X-Ios-Bundle-Identifier": "com.example.other"}),
		"API_KEY_IOS_APP_BLOCKED", itkService, "Requests from this iOS client application com.example.other are blocked.")
	keyBlocked(t, p, p.refresh(up.str("refreshToken")), "API_KEY_IOS_APP_BLOCKED", stsService,
		"Requests from this iOS client application <empty> are blocked.")
}

// The pages Aruma serves in place of Firebase Hosting's check the key as Google's pages make
// their API calls: from the page's own URL. A website restriction must list Aruma's host (as it
// must list firebaseapp.com at Google); app restrictions cannot be met by a browser and are
// left to the calls that hand out tokens.
func TestAuthKeyRestrictionsOnPages(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": up.str("idToken")}))
	path, _ := linkCode(t, p.lastMail("sara@example.com")["link"].(string))
	host := strings.TrimPrefix(e.URL, "http://")

	p.restrict(map[string]any{"browserKeyRestrictions": map[string]any{"allowedReferrers": []string{"https://good.example/*"}}})
	if r := e.page("GET", path, nil, nil); r.Status != 403 || strings.Contains(string(r.Body), "<form") {
		t.Fatalf("action page with a key that does not list Aruma: %d %s", r.Status, r.Body)
	}
	if r := e.page("GET", "/__/auth/iframe?apiKey="+p.key+"&appName=%5BDEFAULT%5D", nil, nil); !strings.Contains(r.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("iframe with a refused key: %s", r.Header.Get("Content-Security-Policy"))
	}
	start := e.page("GET", "/__/auth/handler?"+url.Values{"apiKey": {p.key}, "appName": {"[DEFAULT]"}, "authType": {"signInViaPopup"},
		"providerId": {"google.com"}, "redirectUrl": {"http://localhost:5173/"}, "eventId": {"1"}}.Encode(), nil, nil)
	m := pageDataRe.FindStringSubmatch(string(start.Body))
	if start.Status != 403 || m == nil || !strings.Contains(m[1], `"mode":"error"`) || strings.Contains(m[1], `"authType":"signInViaPopup"`) {
		t.Fatalf("handler with a refused key: %d %s", start.Status, start.Body)
	}

	p.restrict(map[string]any{"browserKeyRestrictions": map[string]any{"allowedReferrers": []string{"https://good.example/*", "http://" + host + "/*"}}})
	pg := e.page("GET", path, nil, nil)
	if pg.Status != 200 {
		t.Fatalf("action page with Aruma listed: %d %s", pg.Status, pg.Body)
	}
	if r := e.page("GET", "/__/auth/iframe?apiKey="+p.key+"&appName=%5BDEFAULT%5D", nil, nil); strings.Contains(r.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("iframe with Aruma listed: %s", r.Header.Get("Content-Security-Policy"))
	}

	// An iOS key still opens the link from the mail; signing in from the app is what it guards.
	p.restrict(map[string]any{"iosKeyRestrictions": map[string]any{"allowedBundleIds": []string{"com.example.chat"}}})
	if done := e.page("POST", "/__/auth/action", formOf(t, pg), nil); done.Status != 200 {
		t.Fatalf("confirm with an iOS key: %d %s", done.Status, done.Body)
	}
	// An API restriction applies to the pages too.
	p.restrict(map[string]any{"apiTargets": []map[string]any{{"service": "firestore.googleapis.com"}}})
	if r := e.page("GET", path, nil, nil); r.Status != 403 {
		t.Fatalf("action page with a Firestore-only key: %d", r.Status)
	}
}

// The console validates restrictions as Google's API Keys API shapes them.
func TestAuthKeyRestrictionsValidation(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	keys := p.owner.mustOK(p.owner.do("GET", "/console/v1/projects/chat-app/apiKeys", nil)).list("keys")
	uid := keys[0]["uid"].(string)
	patch := func(r map[string]any) *Response {
		return p.owner.do("PATCH", "/console/v1/projects/chat-app/apiKeys/"+uid, map[string]any{"restrictions": r})
	}
	for _, bad := range []map[string]any{
		{"browserKeyRestrictions": map[string]any{"allowedReferrers": []string{"mysubdomain*.example.com"}}},
		{"browserKeyRestrictions": map[string]any{"allowedReferrers": []string{"example.com/*/x"}}},
		{"serverKeyRestrictions": map[string]any{"allowedIps": []string{"10.0.0.0/33"}}},
		{"serverKeyRestrictions": map[string]any{"allowedIps": []string{"1.2.3.4"}}, "iosKeyRestrictions": map[string]any{"allowedBundleIds": []string{"com.a.b"}}},
		{"apiTargets": []map[string]any{{"service": "identitytoolkit.googleapis.com", "methods": []string{"*"}}}},
	} {
		expect(t, patch(bad), 400, "INVALID_ARGUMENT")
	}
	got := p.owner.mustOK(patch(map[string]any{"serverKeyRestrictions": map[string]any{"allowedIps": []string{"198.51.100.0/24", "2001:db8::1"}}}))
	if !reflect.DeepEqual(got.JSON["restrictions"], map[string]any{"serverKeyRestrictions": map[string]any{"allowedIps": []any{"198.51.100.0/24", "2001:db8::1"}}}) {
		t.Fatalf("stored restrictions: %s", got.Body)
	}
}
