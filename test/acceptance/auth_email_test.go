package acceptance

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/smtptest"
)

// Email actions: verification, password reset, email change and recovery, the action page,
// the dev mailbox and SMTP delivery (AC-A6, AC-A7, AC-A19, part of AC-A4, and the tamper
// scenarios on email codes).

// callWith posts a client request with extra headers, such as X-Firebase-Locale.
func (p *authProject) callWith(path string, body any, headers map[string]string) *Response {
	p.env.t.Helper()
	c := p.env.client()
	for k, v := range headers {
		c.header[k] = v
	}
	return c.do("POST", path+"?key="+p.key, body)
}

// mailbox returns the project's dev mailbox, newest first.
func (p *authProject) mailbox() []map[string]any {
	p.env.t.Helper()
	return p.owner.mustOK(p.owner.do("GET", "/console/v1/projects/"+p.id+"/auth/mailbox", nil)).list("messages")
}

// lastMail returns the newest dev mailbox message to an address.
func (p *authProject) lastMail(to string) map[string]any {
	p.env.t.Helper()
	for _, m := range p.mailbox() {
		if m["to"] == to {
			return m
		}
	}
	p.env.t.Fatalf("no message to %s in the dev mailbox", to)
	return nil
}

func linkCode(t *testing.T, link string) (path string, q url.Values) {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path + "?" + u.RawQuery, u.Query()
}

// page fetches a page of the server as a browser would.
func (e *Env) page(method, path string, form url.Values, headers map[string]string) *Response {
	e.t.Helper()
	var req *http.Request
	var err error
	if form != nil {
		req, err = http.NewRequest(method, e.URL+path, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequest(method, e.URL+path, nil)
	}
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return decodeResponse(e.t, resp)
}

var hiddenRe = regexp.MustCompile(`<input type="hidden" name="(\w+)" value="([^"]*)">`)

// formOf reads the hidden fields of the action page's form.
func formOf(t *testing.T, r *Response) url.Values {
	t.Helper()
	m := hiddenRe.FindAllStringSubmatch(string(r.Body), -1)
	if len(m) == 0 {
		t.Fatalf("no form on the page: %s", r.Body)
	}
	f := url.Values{}
	for _, kv := range m {
		f.Set(kv[1], html.UnescapeString(kv[2]))
	}
	return f
}

func lookupUser(t *testing.T, p *authProject, idToken string) map[string]any {
	t.Helper()
	return ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": idToken})).list("users")[0]
}

// AC-A6: verification end to end through the dev mailbox, with the link opened on the action
// page; the same flow through applyActionCode from the SDK.
func TestAuthEmailVerificationThroughDevMailbox(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	if lookupUser(t, p, up.str("idToken"))["emailVerified"] != false {
		t.Fatal("a new user starts verified")
	}

	sent := ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{
		"requestType": "VERIFY_EMAIL", "idToken": up.str("idToken"), "continueUrl": "http://localhost:3000/done",
	}))
	if sent.str("kind") != "identitytoolkit#GetOobConfirmationCodeResponse" || sent.str("email") != "sara@example.com" {
		t.Fatalf("sendOobCode: %s", sent.Body)
	}
	box := p.owner.mustOK(p.owner.do("GET", "/console/v1/projects/chat-app/auth/mailbox", nil))
	if box.JSON["enabled"] != true {
		t.Fatalf("dev mailbox not enabled on a loopback instance: %s", box.Body)
	}
	m := p.lastMail("sara@example.com")
	if m["type"] != "VERIFY_EMAIL" || !strings.Contains(m["subject"].(string), "أكّد") || !strings.Contains(m["html"].(string), `dir="rtl"`) {
		t.Fatalf("message: %v", m)
	}
	path, q := linkCode(t, m["link"].(string))
	if !strings.HasPrefix(m["link"].(string), e.URL+"/__/auth/action?") || q.Get("mode") != "verifyEmail" || q.Get("apiKey") != p.key ||
		q.Get("lang") != "ar" || q.Get("continueUrl") != "http://localhost:3000/done" || len(q.Get("oobCode")) < 40 {
		t.Fatalf("link: %s", m["link"])
	}
	if !strings.Contains(m["text"].(string), m["link"].(string)) {
		t.Fatalf("the plain-text part lacks the link: %q", m["text"])
	}

	pg := e.page("GET", path, nil, nil)
	if pg.Status != 200 || !strings.Contains(string(pg.Body), `dir="rtl"`) {
		t.Fatalf("action page: %d %s", pg.Status, pg.Body)
	}
	// Opening the link changes nothing: a mail scanner's prefetch cannot use the code.
	if lookupUser(t, p, up.str("idToken"))["emailVerified"] != false {
		t.Fatal("opening the link verified the email without a click")
	}
	form := formOf(t, pg)
	done := e.page("POST", "/__/auth/action", form, nil)
	if done.Status != 200 || !strings.Contains(string(done.Body), `href="http://localhost:3000/done"`) {
		t.Fatalf("confirm: %d %s", done.Status, done.Body)
	}
	if lookupUser(t, p, up.str("idToken"))["emailVerified"] != true {
		t.Fatal("emailVerified is still false")
	}
	if again := e.page("POST", "/__/auth/action", form, nil); again.Status != 400 || !strings.Contains(string(again.Body), "استُخدم") {
		t.Fatalf("a used code was accepted again: %d %s", again.Status, again.Body)
	}

	// The SDK path: getOobConfirmationCode (v3), English by X-Firebase-Locale, then
	// accounts:update {oobCode}.
	other := p.signUp("omar@example.com", "secret1")
	ok(t, p.callWith(rp+"getOobConfirmationCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": other.str("idToken")}, map[string]string{"X-Firebase-Locale": "en-US"}))
	m = p.lastMail("omar@example.com")
	_, q = linkCode(t, m["link"].(string))
	if m["subject"] != "Confirm your email for Chat-app" || q.Get("lang") != "en" {
		t.Fatalf("English message: %v", m)
	}
	info := ok(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": q.Get("oobCode")}))
	if info.str("requestType") != "VERIFY_EMAIL" || info.str("email") != "omar@example.com" {
		t.Fatalf("checkActionCode: %s", info.Body)
	}
	applied := ok(t, p.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}))
	if applied.JSON["emailVerified"] != true || applied.str("email") != "omar@example.com" || applied.str("localId") != other.str("localId") {
		t.Fatalf("applyActionCode: %s", applied.Body)
	}
	authErr(t, p.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}), "INVALID_OOB_CODE")
	authErr(t, p.call(itk+"accounts:update", map[string]any{"oobCode": "not-a-code"}), "INVALID_OOB_CODE")
}

// AC-A7: reset end to end. The new password works, the old one does not, and refresh tokens
// from before the reset answer TOKEN_EXPIRED.
func TestAuthPasswordReset(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	p.ageSessions()

	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "Sara@example.com"}))
	m := p.lastMail("sara@example.com")
	_, q := linkCode(t, m["link"].(string))
	if m["type"] != "PASSWORD_RESET" || q.Get("mode") != "resetPassword" {
		t.Fatalf("message: %v", m)
	}
	code := q.Get("oobCode")
	info := ok(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": code}))
	if info.str("kind") != "identitytoolkit#ResetPasswordResponse" || info.str("requestType") != "PASSWORD_RESET" || info.str("email") != "sara@example.com" {
		t.Fatalf("verifyPasswordResetCode: %s", info.Body)
	}
	authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": code, "newPassword": "123"}), "WEAK_PASSWORD")
	authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"newPassword": "new-secret-1"}), "MISSING_OOB_CODE")
	// A failed attempt does not use the code.
	ok(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": code, "newPassword": "new-secret-1"}))

	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1"}), "INVALID_LOGIN_CREDENTIALS")
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "new-secret-1"}))
	authErr(t, p.refresh(up.str("refreshToken")), "TOKEN_EXPIRED")
	ok(t, p.refresh(in.str("refreshToken")))
	if lookupUser(t, p, in.str("idToken"))["emailVerified"] != true {
		t.Fatal("a reset through the mailbox should verify the email")
	}
	authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": code, "newPassword": "third-secret"}), "INVALID_OOB_CODE")
	// A reset code cannot be applied as a verification.
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com"}))
	_, q = linkCode(t, p.lastMail("sara@example.com")["link"].(string))
	authErr(t, p.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}), "INVALID_OOB_CODE")

	// The same through the action page, in English.
	path, _ := linkCode(t, p.lastMail("sara@example.com")["link"].(string))
	pg := e.page("GET", strings.Replace(path, "lang=ar", "lang=en", 1), nil, nil)
	if pg.Status != 200 || !strings.Contains(string(pg.Body), `type="password"`) || !strings.Contains(string(pg.Body), "sara@example.com") {
		t.Fatalf("reset page: %d %s", pg.Status, pg.Body)
	}
	form := formOf(t, pg)
	form.Set("newPassword", "page-secret-1")
	form.Set("confirmPassword", "page-secret-2")
	if r := e.page("POST", "/__/auth/action", form, nil); r.Status != 400 || !strings.Contains(string(r.Body), "do not match") {
		t.Fatalf("mismatched passwords: %d %s", r.Status, r.Body)
	}
	form.Set("newPassword", "abc")
	form.Set("confirmPassword", "abc")
	if r := e.page("POST", "/__/auth/action", form, nil); r.Status != 400 || !strings.Contains(string(r.Body), "too short") {
		t.Fatalf("weak password: %d %s", r.Status, r.Body)
	}
	form.Set("newPassword", "page-secret-1")
	form.Set("confirmPassword", "page-secret-1")
	p.ageSessions()
	if r := e.page("POST", "/__/auth/action", form, nil); r.Status != 200 || !strings.Contains(string(r.Body), "Your password has changed") {
		t.Fatalf("reset: %d %s", r.Status, r.Body)
	}
	ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "page-secret-1"}))
	authErr(t, p.refresh(in.str("refreshToken")), "TOKEN_EXPIRED")
}

// AC-A19 and the tamper scenarios on links: Arabic and right-to-left by default, English by
// lang, Aruma's identity, strict headers, no third-party resources; a continueUrl off the
// authorised domains is refused and never offered; a key of another project, a forged form
// token and a cross-site post are refused.
func TestAuthActionPage(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	p.signUp("sara@example.com", "secret1")

	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com", "continueUrl": "https://evil.example/x"}), "UNAUTHORIZED_DOMAIN")
	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com", "continueUrl": "not a url"}), "INVALID_CONTINUE_URI")
	if len(p.mailbox()) != 0 {
		t.Fatal("a refused request sent mail")
	}
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com", "continueUrl": "http://localhost:3000/after"}))
	path, q := linkCode(t, p.lastMail("sara@example.com")["link"].(string))

	ar := e.page("GET", path, nil, nil)
	body := string(ar.Body)
	for _, want := range []string{`<html lang="ar" dir="rtl">`, "أروما", "غير تابع لشركة Google ولا لـ Firebase", "اختر كلمة مرور جديدة"} {
		if !strings.Contains(body, want) {
			t.Errorf("Arabic page lacks %q", want)
		}
	}
	csp := ar.Header.Get("Content-Security-Policy")
	if !strings.HasPrefix(csp, "default-src 'none'; style-src 'sha256-") || strings.Contains(csp, "unsafe") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP: %s", csp)
	}
	if ar.Header.Get("Referrer-Policy") != "no-referrer" || ar.Header.Get("X-Frame-Options") != "DENY" || ar.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", ar.Header)
	}
	if strings.Contains(body, "<script") || regexp.MustCompile(`(src|href)="https?://`).MatchString(body) {
		t.Error("the page loads a script or a third-party resource")
	}

	q.Set("lang", "en")
	en := e.page("GET", "/__/auth/action?"+q.Encode(), nil, nil)
	for _, want := range []string{`<html lang="en" dir="ltr">`, ">Aruma<", "not affiliated with or endorsed by Google or Firebase", "Choose a new password"} {
		if !strings.Contains(string(en.Body), want) {
			t.Errorf("English page lacks %q", want)
		}
	}

	// A continueUrl swapped in the link is ignored: only the one stored with the code counts.
	q.Set("continueUrl", "https://evil.example/phish")
	swapped := e.page("GET", "/__/auth/action?"+q.Encode(), nil, nil)
	form := formOf(t, swapped)
	form.Set("newPassword", "new-secret-1")
	form.Set("confirmPassword", "new-secret-1")

	forged := url.Values{}
	for k, v := range form {
		forged[k] = v
	}
	forged.Set("token", "forged")
	if r := e.page("POST", "/__/auth/action", forged, nil); r.Status != 403 {
		t.Fatalf("forged form token: %d", r.Status)
	}
	if r := e.page("POST", "/__/auth/action", form, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); r.Status != 403 {
		t.Fatalf("cross-site post: %d", r.Status)
	}
	done := e.page("POST", "/__/auth/action", form, nil)
	if done.Status != 200 || strings.Contains(string(done.Body), "evil.example") || !strings.Contains(string(done.Body), `href="http://localhost:3000/after"`) {
		t.Fatalf("continue: %d %s", done.Status, done.Body)
	}

	// A continueUrl whose domain is no longer authorised is no longer offered.
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com", "continueUrl": "http://localhost:3000/after"}))
	if _, err := e.Pool.Exec(context.Background(), `UPDATE auth.project_config SET authorized_domains = '{example.com}' WHERE project_number = $1`, p.number); err != nil {
		t.Fatal(err)
	}
	path, _ = linkCode(t, p.lastMail("sara@example.com")["link"].(string))
	form = formOf(t, e.page("GET", path, nil, nil))
	form.Set("newPassword", "new-secret-2")
	form.Set("confirmPassword", "new-secret-2")
	if r := e.page("POST", "/__/auth/action", form, nil); r.Status != 200 || strings.Contains(string(r.Body), "localhost:3000") {
		t.Fatalf("de-authorised continueUrl offered: %d %s", r.Status, r.Body)
	}

	// The code belongs to its project: another project's key makes it unknown.
	p.owner.project("other-app")
	web := p.owner.app("other-app", "WEB", "")
	otherKey := p.owner.mustOK(p.owner.do("GET", "/console/v1/projects/other-app/apiKeys/"+web.str("apiKeyId"), nil)).str("keyString")
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com"}))
	_, q = linkCode(t, p.lastMail("sara@example.com")["link"].(string))
	q.Set("apiKey", otherKey)
	if r := e.page("GET", "/__/auth/action?"+q.Encode(), nil, nil); r.Status != 400 || strings.Contains(string(r.Body), "<form") {
		t.Fatalf("another project's key: %d %s", r.Status, r.Body)
	}
	resp, err := http.Post(e.URL+itk+"accounts:resetPassword?key="+otherKey, "application/json", strings.NewReader(fmt.Sprintf(`{"oobCode":%q}`, q.Get("oobCode"))))
	if err != nil {
		t.Fatal(err)
	}
	authErr(t, decodeResponse(t, resp), "INVALID_OOB_CODE")
	// A mode that does not match the code is an invalid link.
	q.Set("apiKey", p.key)
	q.Set("mode", "verifyEmail")
	if r := e.page("GET", "/__/auth/action?"+q.Encode(), nil, nil); r.Status != 400 {
		t.Fatalf("mismatched mode: %d", r.Status)
	}
}

// Used and expired codes are refused, by the API and on the page.
func TestAuthEmailCodeExpiry(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": up.str("idToken")}))
	path, q := linkCode(t, p.lastMail("sara@example.com")["link"].(string))
	if _, err := e.Pool.Exec(context.Background(), `UPDATE auth.oob_codes SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	authErr(t, p.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}), "EXPIRED_OOB_CODE")
	authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": q.Get("oobCode")}), "EXPIRED_OOB_CODE")
	if r := e.page("GET", path, nil, nil); r.Status != 400 || !strings.Contains(string(r.Body), "انتهت صلاحية") {
		t.Fatalf("expired link page: %d %s", r.Status, r.Body)
	}
	// Codes stop working once the user is disabled.
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": up.str("idToken")}))
	_, q = linkCode(t, p.lastMail("sara@example.com")["link"].(string))
	if _, err := e.Pool.Exec(context.Background(), `UPDATE auth.users SET disabled = true`); err != nil {
		t.Fatal(err)
	}
	authErr(t, p.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}), "USER_DISABLED")
}

// AC-A4 (email actions): with enumeration protection on, an unknown address for a reset and a
// taken one for a change both succeed without sending; with it off they are reported.
func TestAuthEmailEnumerationProtection(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	p.signUp("taken@example.com", "secret1")

	unknown := ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "nobody@example.com"}))
	if unknown.str("email") != "nobody@example.com" {
		t.Fatalf("response: %s", unknown.Body)
	}
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_AND_CHANGE_EMAIL", "idToken": up.str("idToken"), "newEmail": "taken@example.com"}))
	if n := len(p.mailbox()); n != 0 {
		t.Fatalf("%d messages sent for an unknown or taken address", n)
	}
	// A direct email change is refused under protection; the verified path is the way.
	authErr(t, p.call(itk+"accounts:update", map[string]any{"idToken": up.str("idToken"), "email": "new@example.com"}), "OPERATION_NOT_ALLOWED")

	if _, err := e.Pool.Exec(context.Background(), `UPDATE auth.project_config SET enumeration_protection = false WHERE project_number = $1`, p.number); err != nil {
		t.Fatal(err)
	}
	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "nobody@example.com"}), "EMAIL_NOT_FOUND")
	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_AND_CHANGE_EMAIL", "idToken": up.str("idToken"), "newEmail": "taken@example.com"}), "EMAIL_EXISTS")

	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"email": "sara@example.com"}), "MISSING_REQ_TYPE")
	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "EMAIL_SIGNIN", "email": "sara@example.com"}), "OPERATION_NOT_ALLOWED")
	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "bad"}), "INVALID_EMAIL")
	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL"}), "INVALID_ID_TOKEN")
	// Only the Admin API may have the link returned instead of mailed.
	r := p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com", "returnOobLink": true})
	if r.Status != 403 || strings.Contains(string(r.Body), "oobLink") {
		t.Fatalf("returnOobLink from a client: %d %s", r.Status, r.Body)
	}
}

// verifyBeforeUpdateEmail end to end: the change happens when the new address confirms, the
// sessions are revoked, and the old address gets a link that restores it.
func TestAuthChangeAndRecoverEmail(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	p.ageSessions()
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_AND_CHANGE_EMAIL", "idToken": up.str("idToken"), "newEmail": "sara.new@example.com"}))
	m := p.lastMail("sara.new@example.com")
	if m["type"] != "VERIFY_AND_CHANGE_EMAIL" || !strings.Contains(m["text"].(string), "sara@example.com") || !strings.Contains(m["text"].(string), "sara.new@example.com") {
		t.Fatalf("change message: %v", m)
	}
	_, q := linkCode(t, m["link"].(string))
	info := ok(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": q.Get("oobCode")}))
	if info.str("requestType") != "VERIFY_AND_CHANGE_EMAIL" || info.str("email") != "sara@example.com" || info.str("newEmail") != "sara.new@example.com" {
		t.Fatalf("checkActionCode: %s", info.Body)
	}
	applied := ok(t, p.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}))
	if applied.str("email") != "sara.new@example.com" || applied.JSON["emailVerified"] != true {
		t.Fatalf("change: %s", applied.Body)
	}
	authErr(t, p.refresh(up.str("refreshToken")), "TOKEN_EXPIRED")
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara.new@example.com", "password": "secret1"}))

	rec := p.lastMail("sara@example.com")
	path, q := linkCode(t, rec["link"].(string))
	if rec["type"] != "RECOVER_EMAIL" || q.Get("mode") != "recoverEmail" {
		t.Fatalf("recovery message: %v", rec)
	}
	pg := e.page("GET", path, nil, nil)
	if !strings.Contains(string(pg.Body), "sara@example.com") {
		t.Fatalf("recovery page: %s", pg.Body)
	}
	p.ageSessions()
	if r := e.page("POST", "/__/auth/action", formOf(t, pg), nil); r.Status != 200 {
		t.Fatalf("recover: %d %s", r.Status, r.Body)
	}
	authErr(t, p.refresh(in.str("refreshToken")), "TOKEN_EXPIRED")
	again := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1"}))
	if u := lookupUser(t, p, again.str("idToken")); u["email"] != "sara@example.com" || u["emailVerified"] != true {
		t.Fatalf("after recovery: %v", u)
	}
	// The change link was for the address the account had then; it cannot be replayed.
	authErr(t, p.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}), "INVALID_OOB_CODE")
}

// Flood limits: per recipient (a reset answers RESET_PASSWORD_EXCEED_LIMIT, whether or not the
// account exists), and per client address.
func TestAuthEmailFloodLimits(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	p.signUp("sara@example.com", "secret1")
	for _, addr := range []string{"sara@example.com", "nobody@example.com"} {
		for i := 0; i < 5; i++ {
			ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": addr}))
		}
		authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": strings.ToUpper(addr)}), "RESET_PASSWORD_EXCEED_LIMIT")
	}
	if n := len(p.mailbox()); n != 5 {
		t.Fatalf("%d messages, want 5", n)
	}
	// 12 requests so far; the per-address bucket holds 20.
	for i := 0; i < 8; i++ {
		ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": fmt.Sprintf("user%d@example.com", i)}))
	}
	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "late@example.com"}), "TOO_MANY_ATTEMPTS_TRY_LATER")
}

// SMTP delivery: settings with a sealed password, the outbox worker with retries, and the test
// message; the dev mailbox stays empty once the project has an SMTP host.
func TestAuthEmailSMTPDelivery(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	srv := smtptest.Start(t, nil)
	smtp := "/console/v1/projects/chat-app/auth/smtp"
	set := p.owner.mustOK(p.owner.do("PATCH", smtp, map[string]any{
		"host": srv.Host, "port": srv.Port, "tls": "none", "username": "mailer", "password": "smtp-pass-1",
		"senderAddress": "noreply@chat.example", "senderName": "Chat", "replyTo": "help@chat.example",
	}))
	if set.JSON["passwordSet"] != true || set.JSON["devMailbox"] != false || set.JSON["password"] != nil || set.JSON["port"] != float64(srv.Port) {
		t.Fatalf("smtp settings: %s", set.Body)
	}
	expect(t, p.owner.do("PATCH", smtp, map[string]any{"tls": "sometimes"}), 400, "INVALID_ARGUMENT")
	expect(t, p.owner.do("PATCH", smtp, map[string]any{"senderAddress": "x\r\nBcc: a@b.c"}), 400, "INVALID_ARGUMENT")

	up := p.signUp("sara@example.com", "secret1")
	p.ageSessions()
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com"}))
	box := p.owner.mustOK(p.owner.do("GET", "/console/v1/projects/chat-app/auth/mailbox", nil))
	if box.JSON["enabled"] != false || len(box.list("messages")) != 0 {
		t.Fatalf("mailbox with SMTP configured: %s", box.Body)
	}
	if n, err := e.App.Auth.DeliverMail(context.Background()); err != nil || n != 1 {
		t.Fatalf("DeliverMail = %d, %v", n, err)
	}
	got := srv.Messages()
	if len(got) != 1 || got[0].From != "noreply@chat.example" || got[0].To[0] != "sara@example.com" || got[0].Auth != "mailer:smtp-pass-1" {
		t.Fatalf("received %+v", got)
	}
	link := regexp.MustCompile(`href=3D"([^"]+)"`).FindStringSubmatch(got[0].Data)
	if link == nil {
		t.Fatalf("no link in %s", got[0].Data)
	}
	raw := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(link[1], "=\r\n", ""), "=3D", "="), "&amp;", "&")
	_, q := linkCode(t, raw)
	ok(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": q.Get("oobCode"), "newPassword": "smtp-reset-1"}))
	authErr(t, p.refresh(up.str("refreshToken")), "TOKEN_EXPIRED")

	// A temporary failure is retried later, with the error kept on the row.
	srv.FailNext(1)
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com"}))
	if n, _ := e.App.Auth.DeliverMail(context.Background()); n != 0 {
		t.Fatal("a failed delivery counted as sent")
	}
	var attempts int
	var lastErr string
	if err := e.Pool.QueryRow(context.Background(), `SELECT attempts, last_error FROM auth.email_outbox WHERE sent_at IS NULL`).Scan(&attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !strings.Contains(lastErr, "451") {
		t.Fatalf("attempts %d, last error %q", attempts, lastErr)
	}
	if n, _ := e.App.Auth.DeliverMail(context.Background()); n != 0 {
		t.Fatal("a message was retried before its backoff")
	}
	if _, err := e.Pool.Exec(context.Background(), `UPDATE auth.email_outbox SET next_attempt_at = now() WHERE sent_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.App.Auth.DeliverMail(context.Background()); n != 1 || len(srv.Messages()) != 2 {
		t.Fatalf("retry: sent %d, received %d", n, len(srv.Messages()))
	}

	test := p.owner.mustOK(p.owner.do("POST", smtp+":test", map[string]any{"to": "owner@example.com"}))
	if test.JSON["devMailbox"] != false || len(srv.Messages()) != 3 || srv.Messages()[2].To[0] != "owner@example.com" {
		t.Fatalf("test message: %s, received %d", test.Body, len(srv.Messages()))
	}

	// Removing the host puts the project back on the dev mailbox.
	back := p.owner.mustOK(p.owner.do("PATCH", smtp, map[string]any{"host": "", "password": ""}))
	if back.JSON["devMailbox"] != true || back.JSON["passwordSet"] != false {
		t.Fatalf("after clearing: %s", back.Body)
	}
	if r := p.owner.mustOK(p.owner.do("POST", smtp+":test", map[string]any{"to": "owner@example.com"})); r.JSON["devMailbox"] != true || len(p.mailbox()) != 1 {
		t.Fatalf("test to the dev mailbox: %s", r.Body)
	}
}

// The console's template, mailbox and reset routes (docs/reference/console-api.md).
func TestAuthEmailConsole(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	base := "/console/v1/projects/chat-app/auth/templates"
	list := p.owner.mustOK(p.owner.do("GET", base, nil)).list("templates")
	if len(list) != 8 || list[0]["type"] != "VERIFY_EMAIL" || list[0]["language"] != "ar" || list[0]["customized"] != false {
		t.Fatalf("templates: %v", list)
	}
	expect(t, p.owner.do("PUT", base+"/PASSWORD_RESET/ar", map[string]any{"subject": "x", "body": "no link"}), 400, "INVALID_ARGUMENT")
	expect(t, p.owner.do("PUT", base+"/PASSWORD_RESET/fr", map[string]any{"subject": "x", "body": "%LINK%"}), 400, "INVALID_ARGUMENT")
	put := p.owner.mustOK(p.owner.do("PUT", base+"/PASSWORD_RESET/ar", map[string]any{
		"subject": "استعادة %APP_NAME%", "body": `<p>مرحبًا %DISPLAY_NAME% <a href="%LINK%">اضغط</a> %EMAIL%</p>`,
	}))
	if put.JSON["customized"] != true {
		t.Fatalf("put: %s", put.Body)
	}
	up := p.call(itk+"accounts:signUp", map[string]any{"email": "sara@example.com", "password": "secret1", "displayName": "<i>Sara</i>"})
	ok(t, up)
	// The name goes only to a verified address (security review v0.1, S-4).
	p.sql(`UPDATE auth.users SET email_verified = true WHERE uid = $1`, up.str("localId"))
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com"}))
	m := p.lastMail("sara@example.com")
	if m["subject"] != "استعادة Chat-app" || !strings.Contains(m["html"].(string), "&lt;i&gt;Sara&lt;/i&gt;") || !strings.Contains(m["text"].(string), "اضغط: "+m["link"].(string)) {
		t.Fatalf("custom template: %v", m)
	}
	reset := p.owner.mustOK(p.owner.do("DELETE", base+"/PASSWORD_RESET/ar", nil))
	if reset.JSON["customized"] != false || !strings.Contains(reset.str("body"), "%LINK%") {
		t.Fatalf("reset: %s", reset.Body)
	}

	link := p.owner.mustOK(p.owner.do("POST", "/console/v1/projects/chat-app/auth/users/"+up.str("localId")+":sendPasswordReset", map[string]any{"language": "en"})).str("link")
	if !strings.Contains(link, "mode=resetPassword") || p.lastMail("sara@example.com")["link"] != link {
		t.Fatalf("sendPasswordReset link %q", link)
	}
	expect(t, p.owner.do("POST", "/console/v1/projects/chat-app/auth/users/nobody:sendPasswordReset", nil), 404, "NOT_FOUND")

	p.owner.mustOK(p.owner.do("DELETE", "/console/v1/projects/chat-app/auth/mailbox", nil))
	if len(p.mailbox()) != 0 {
		t.Fatal("mailbox not cleared")
	}
	// Another account cannot reach the project's mail.
	stranger := e.account("eve@example.com")
	expect(t, stranger.do("GET", "/console/v1/projects/chat-app/auth/mailbox", nil), 404, "NOT_FOUND")
	expect(t, stranger.do("GET", "/console/v1/projects/chat-app/auth/smtp", nil), 404, "NOT_FOUND")
}

// Outside loopback the dev mailbox does not exist unless configured.
func TestAuthEmailNoDevMailboxInProduction(t *testing.T) {
	e := newEnv(t, map[string]string{"ARUMA_MAIL_DEV_MAILBOX": "false"})
	p := e.authProject("chat-app")
	expect(t, p.owner.do("GET", "/console/v1/projects/chat-app/auth/mailbox", nil), 404, "NOT_FOUND")
	expect(t, p.owner.do("PATCH", "/console/v1/projects/chat-app/auth/smtp", map[string]any{"devMailbox": true}), 400, "INVALID_ARGUMENT")
	p.signUp("sara@example.com", "secret1")
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com"}))
	var dev bool
	if err := e.Pool.QueryRow(context.Background(), `SELECT dev_mailbox FROM auth.email_outbox`).Scan(&dev); err != nil || dev {
		t.Fatalf("message kept for a dev mailbox in production: %v %v", dev, err)
	}
	// Without SMTP the worker cannot send: the message waits, with the reason recorded.
	if n, _ := e.App.Auth.DeliverMail(context.Background()); n != 0 {
		t.Fatal("sent without SMTP")
	}
	var lastErr string
	if err := e.Pool.QueryRow(context.Background(), `SELECT last_error FROM auth.email_outbox`).Scan(&lastErr); err != nil || !strings.Contains(lastErr, "no SMTP server") {
		t.Fatalf("last error %q, %v", lastErr, err)
	}
}

// Tamper scenario "dump of DB and logs": email codes, links and the SMTP password never appear
// in the database or the log in a usable form.
func TestAuthEmailNoRawSecretsStored(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("sara@example.com", "secret1")
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": up.str("idToken")}))
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com"}))
	var codes []string
	for _, m := range p.mailbox() {
		path, q := linkCode(t, m["link"].(string))
		codes = append(codes, q.Get("oobCode"))
		e.page("GET", path, nil, nil) // the code travels in the page's query string
	}
	p.owner.mustOK(p.owner.do("PATCH", "/console/v1/projects/chat-app/auth/smtp", map[string]any{"password": "smtp-secret-pw", "devMailbox": true}))

	dump := dumpSchema(t, e, "auth")
	logs := e.Logs.String()
	if !strings.Contains(dump, "sara@example.com") || len(codes) != 2 {
		t.Fatal("the scenario left no public data or codes to look for")
	}
	for _, s := range append(codes, "smtp-secret-pw") {
		if strings.Contains(dump, s) {
			t.Errorf("%q found in the database", s)
		}
		if strings.Contains(logs, s) {
			t.Errorf("%q found in the server log", s)
		}
	}
}

// dumpSchema renders every row of a schema's tables as text.
func dumpSchema(t *testing.T, e *Env, schema string) string {
	t.Helper()
	ctx := context.Background()
	rows, err := e.Pool.Query(ctx, `SELECT schemaname || '.' || tablename FROM pg_tables WHERE schemaname = $1`, schema)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	var b strings.Builder
	for _, table := range tables {
		r, err := e.Pool.Query(ctx, fmt.Sprintf(`SELECT t::text FROM %s t`, table))
		if err != nil {
			t.Fatal(err)
		}
		for r.Next() {
			var line string
			_ = r.Scan(&line)
			b.WriteString(line + "\n")
		}
		r.Close()
	}
	return b.String()
}

// The Android SDK sends requestType as the enum's number (1 = PASSWORD_RESET), which
// protobuf's JSON mapping allows alongside the name.
func TestAuthOobRequestTypeAsNumber(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	p.signUp("sara@example.com", "secret1")
	r := ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": 1, "email": "sara@example.com"}))
	assertEqual(t, "email", r.str("email"), "sara@example.com")
	if m := p.lastMail("sara@example.com"); m["type"] != "PASSWORD_RESET" {
		t.Fatalf("mail: %v", m)
	}
	authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": 99, "email": "sara@example.com"}), "INVALID_JSON")
}
