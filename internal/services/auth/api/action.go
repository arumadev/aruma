package api

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/platform/ratelimit"
	"github.com/osama300/aurma-project/internal/services/auth/core"
)

// The action page, /__/auth/action, is where the links in Auth emails land (research §6). It
// is rendered on the server with no script at all: Arabic and right-to-left unless lang=en,
// Aruma's name and a line saying it is not Google's. Opening a link never changes anything by
// itself: the page asks for a click (or the new password), so mail scanners that prefetch
// links cannot use the code. The form posts back here with a token bound to the code, behind
// a cross-origin check. "Continue" only ever leads to the URL stored with the code, re-checked
// against the authorised domains.

const actionCSS = `body{margin:0;background:#f6f7f9;color:#1d2330;font:16px/1.6 system-ui,-apple-system,"Segoe UI",Tahoma,Arial,sans-serif}` +
	`main{max-width:28rem;margin:3rem auto;padding:2rem;background:#fff;border:1px solid #dde1e7;border-radius:12px}` +
	`.brand{font-weight:700;color:#0f5c4d;margin:0 0 1.5rem}h1{font-size:1.35rem;margin:0 0 1rem}` +
	`label{display:block;margin:1rem 0 .25rem}input{box-sizing:border-box;width:100%;padding:.6rem;font:inherit;border:1px solid #b9c0cb;border-radius:8px}` +
	`button,.continue{display:inline-block;margin-top:1.25rem;padding:.6rem 1.2rem;font:inherit;color:#fff;background:#0f5c4d;border:0;border-radius:8px;text-decoration:none;cursor:pointer}` +
	`.error{color:#a61b1b}footer{max-width:28rem;margin:0 auto 2rem;padding:0 2rem;font-size:.85rem;color:#5b6472}`

// actionCSP allows nothing but the page's own stylesheet, by hash, and posting to itself.
var actionCSP = func() string {
	sum := sha256.Sum256([]byte(actionCSS))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; " +
		"form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
}()

var actionTemplate = template.Must(template.New("action").Parse(`<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}} · {{.T.brand}}</title>
<style>` + actionCSS + `</style>
</head>
<body>
<main>
<p class="brand">{{.T.brand}}</p>
<h1>{{.Title}}</h1>
{{if .Message}}<p>{{.Message}}</p>{{end}}
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
{{with .Form}}<form method="post" action="` + core.ActionPath + `">
<input type="hidden" name="mode" value="{{.Mode}}">
<input type="hidden" name="oobCode" value="{{.Code}}">
<input type="hidden" name="apiKey" value="{{.APIKey}}">
<input type="hidden" name="lang" value="{{.Lang}}">
<input type="hidden" name="token" value="{{.Token}}">
{{if .Password}}<label for="newPassword">{{$.T.newPassword}}</label>
<input id="newPassword" name="newPassword" type="password" autocomplete="new-password" required>
<label for="confirmPassword">{{$.T.confirmPassword}}</label>
<input id="confirmPassword" name="confirmPassword" type="password" autocomplete="new-password" required>
{{end}}<button type="submit">{{.Button}}</button>
</form>{{end}}
{{if .Continue}}<a class="continue" href="{{.Continue}}" rel="noopener noreferrer">{{.T.continue}}</a>{{end}}
</main>
<footer>{{.T.disclaimer}}</footer>
</body>
</html>
`))

var actionText = map[string]map[string]string{
	"ar": {
		"brand":           "أروما",
		"disclaimer":      "أروما غير تابع لشركة Google ولا لـ Firebase، ولا معتمد منهما.",
		"title":           "إجراء على الحساب",
		"resetTitle":      "اختر كلمة مرور جديدة",
		"resetPrompt":     "كلمة مرور جديدة للحساب %s.",
		"newPassword":     "كلمة المرور الجديدة",
		"confirmPassword": "أعد كتابة كلمة المرور",
		"resetButton":     "حفظ كلمة المرور",
		"resetDone":       "تغيّرت كلمة المرور، فسجّل الدخول بها الآن.",
		"verifyTitle":     "تأكيد البريد الإلكتروني",
		"verifyPrompt":    "أكّد أن العنوان %s يخصّك.",
		"verifyButton":    "تأكيد",
		"verifyDone":      "تأكّد بريدك الإلكتروني.",
		"changeTitle":     "تغيير بريد الحساب",
		"changePrompt":    "سيصبح بريد حسابك %s.",
		"changeButton":    "تأكيد التغيير",
		"changeDone":      "أصبح بريد حسابك %s. سجّل الدخول من جديد.",
		"recoverTitle":    "استعادة بريد الحساب",
		"recoverPrompt":   "سيعود بريد حسابك إلى %s.",
		"recoverButton":   "استعادة البريد",
		"recoverDone":     "عاد بريد حسابك إلى %s. ننصحك بتغيير كلمة المرور الآن.",
		"invalid":         "هذا الرابط غير صالح أو استُخدم من قبل. اطلب رابطًا جديدًا.",
		"expired":         "انتهت صلاحية هذا الرابط. اطلب رابطًا جديدًا.",
		"disabled":        "هذا الحساب معطّل.",
		"emailExists":     "هذا العنوان مستخدم في حساب آخر.",
		"mismatch":        "كلمتا المرور غير متطابقتين.",
		"weak":            "كلمة المرور قصيرة أو لا تحقق شروط المشروع.",
		"tooMany":         "محاولات كثيرة. أعد المحاولة بعد قليل.",
		"failed":          "تعذّر إتمام الطلب. أعد المحاولة.",
		"continue":        "متابعة",
	},
	"en": {
		"brand":           "Aruma",
		"disclaimer":      "Aruma is not affiliated with or endorsed by Google or Firebase.",
		"title":           "Account action",
		"resetTitle":      "Choose a new password",
		"resetPrompt":     "A new password for %s.",
		"newPassword":     "New password",
		"confirmPassword": "Type the password again",
		"resetButton":     "Save password",
		"resetDone":       "Your password has changed. Sign in with it now.",
		"verifyTitle":     "Confirm your email",
		"verifyPrompt":    "Confirm that %s is yours.",
		"verifyButton":    "Confirm",
		"verifyDone":      "Your email is confirmed.",
		"changeTitle":     "Change your account email",
		"changePrompt":    "Your account email will become %s.",
		"changeButton":    "Confirm the change",
		"changeDone":      "Your account email is now %s. Sign in again.",
		"recoverTitle":    "Restore your account email",
		"recoverPrompt":   "Your account email will go back to %s.",
		"recoverButton":   "Restore email",
		"recoverDone":     "Your account email is %s again. We recommend changing your password now.",
		"invalid":         "This link is invalid or has already been used. Ask for a new one.",
		"expired":         "This link has expired. Ask for a new one.",
		"disabled":        "This account is disabled.",
		"emailExists":     "This address is used by another account.",
		"mismatch":        "The passwords do not match.",
		"weak":            "The password is too short or does not meet the project's requirements.",
		"tooMany":         "Too many attempts. Try again shortly.",
		"failed":          "The request could not be completed. Try again.",
		"continue":        "Continue",
	},
}

// actionModes maps a link's mode to the request type its code must have, and the text keys.
var actionModes = map[string]struct{ kind, key string }{
	"resetPassword":        {core.MailPasswordReset, "reset"},
	"verifyEmail":          {core.MailVerifyEmail, "verify"},
	"verifyAndChangeEmail": {core.MailChangeEmail, "change"},
	"recoverEmail":         {core.MailRecoverEmail, "recover"},
}

type actionView struct {
	Lang, Dir string
	T         map[string]string
	Title     string
	Message   string
	Error     string
	Form      *actionForm
	Continue  string
}

type actionForm struct {
	Mode, Code, APIKey, Lang, Token string
	Password                        bool
	Button                          string
}

// actionRequest is a page request: the link's parameters, from the query or the form.
type actionRequest struct {
	mode, code, key, lang string
}

func newActionView(lang string) *actionView {
	v := &actionView{Lang: "ar", Dir: "rtl"}
	if strings.HasPrefix(strings.ToLower(lang), "en") {
		v.Lang, v.Dir = "en", "ltr"
	}
	v.T = actionText[v.Lang]
	v.Title = v.T["title"]
	return v
}

func (v *actionView) text(key, arg string) string {
	if strings.Contains(v.T[key], "%s") {
		return strings.Replace(v.T[key], "%s", arg, 1)
	}
	return v.T[key]
}

func writeActionPage(w http.ResponseWriter, status int, v *actionView) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", actionCSP)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex")
	w.WriteHeader(status)
	_ = actionTemplate.Execute(w, v)
}

// actionFormProtection refuses form posts from other origins.
func actionFormProtection(h http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := newActionView(r.FormValue("lang"))
		v.Error = v.T["failed"]
		writeActionPage(w, http.StatusForbidden, v)
	}))
	return cop.Handler(h)
}

// begin resolves the project of the link's API key and checks the code against the mode. The
// code must belong to that project: a key of another project makes it unknown.
func (a *API) begin(w http.ResponseWriter, r *http.Request, req actionRequest) (*actionView, core.Project, core.ActionCodeInfo, bool) {
	v := newActionView(req.lang)
	if ok, _ := a.ipLimit.Allow(ratelimit.AddrKey(httpx.ClientAddr(r.Context()))); !ok {
		v.Error = v.T["tooMany"]
		writeActionPage(w, http.StatusTooManyRequests, v)
		return nil, core.Project{}, core.ActionCodeInfo{}, false
	}
	m, known := actionModes[req.mode]
	if !known || req.code == "" || req.key == "" {
		v.Error = v.T["invalid"]
		writeActionPage(w, http.StatusBadRequest, v)
		return nil, core.Project{}, core.ActionCodeInfo{}, false
	}
	v.Title = v.T[m.key+"Title"]
	p, err := a.svc.ProjectForKey(r.Context(), req.key)
	if err != nil {
		v.Error = v.T["invalid"]
		writeActionPage(w, http.StatusBadRequest, v)
		return nil, core.Project{}, core.ActionCodeInfo{}, false
	}
	info, err := a.svc.CheckActionCode(r.Context(), p, req.code)
	if err == nil && info.RequestType != m.kind {
		err = core.ErrInvalidOobCode
	}
	if err != nil {
		a.actionError(w, r, v, err)
		return nil, core.Project{}, core.ActionCodeInfo{}, false
	}
	return v, p, info, true
}

func (a *API) actionError(w http.ResponseWriter, r *http.Request, v *actionView, err error) {
	var e *core.Error
	if !errors.As(err, &e) {
		a.log.Error("action page", "error", err, "request_id", httpx.RequestID(r.Context()))
		v.Error = v.T["failed"]
		writeActionPage(w, http.StatusInternalServerError, v)
		return
	}
	switch e.Code {
	case core.ErrExpiredOobCode.Code:
		v.Error = v.T["expired"]
	case core.ErrUserDisabled.Code:
		v.Error = v.T["disabled"]
	case core.ErrEmailExists.Code:
		v.Error = v.T["emailExists"]
	case core.ErrTooManyAttempts.Code:
		v.Error = v.T["tooMany"]
	case core.ErrInvalidOobCode.Code, core.ErrMissingOobCode.Code:
		v.Error = v.T["invalid"]
	default:
		v.Error = v.T["failed"]
	}
	writeActionPage(w, http.StatusBadRequest, v)
}

func (a *API) form(req actionRequest, v *actionView, info core.ActionCodeInfo) *actionForm {
	key := actionModes[req.mode].key
	return &actionForm{
		Mode: req.mode, Code: req.code, APIKey: req.key, Lang: v.Lang, Token: a.svc.ActionFormToken(req.code),
		Password: info.RequestType == core.MailPasswordReset, Button: v.T[key+"Button"],
	}
}

// subject is the address a mode's prompt and result name.
func subject(info core.ActionCodeInfo) string {
	if info.RequestType == core.MailChangeEmail {
		return info.NewEmail
	}
	return info.Email
}

func (a *API) actionPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := actionRequest{mode: q.Get("mode"), code: q.Get("oobCode"), key: q.Get("apiKey"), lang: q.Get("lang")}
	v, _, info, ok := a.begin(w, r, req)
	if !ok {
		return
	}
	v.Message = v.text(actionModes[req.mode].key+"Prompt", subject(info))
	v.Form = a.form(req, v, info)
	writeActionPage(w, http.StatusOK, v)
}

func (a *API) actionSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		v := newActionView("")
		v.Error = v.T["failed"]
		writeActionPage(w, http.StatusBadRequest, v)
		return
	}
	f := r.PostForm
	req := actionRequest{mode: f.Get("mode"), code: f.Get("oobCode"), key: f.Get("apiKey"), lang: f.Get("lang")}
	if !a.svc.CheckActionFormToken(req.code, f.Get("token")) {
		v := newActionView(req.lang)
		v.Error = v.T["failed"]
		writeActionPage(w, http.StatusForbidden, v)
		return
	}
	v, p, info, ok := a.begin(w, r, req)
	if !ok {
		return
	}
	key := actionModes[req.mode].key
	var err error
	if info.RequestType == core.MailPasswordReset {
		pw := f.Get("newPassword")
		if pw != f.Get("confirmPassword") {
			v.Message, v.Error, v.Form = v.text(key+"Prompt", subject(info)), v.T["mismatch"], a.form(req, v, info)
			writeActionPage(w, http.StatusBadRequest, v)
			return
		}
		var done core.ActionCodeInfo
		done, err = a.svc.ResetPassword(r.Context(), p, req.code, pw)
		if err == nil {
			info = done
		}
		var e *core.Error
		if errors.As(err, &e) && (e.Code == "WEAK_PASSWORD" || e.Code == core.ErrPasswordRequirements.Code) {
			v.Message, v.Error, v.Form = v.text(key+"Prompt", subject(info)), v.T["weak"], a.form(req, v, info)
			if v.Lang == "en" && e.Detail != "" {
				v.Error += " " + e.Detail + "."
			}
			writeActionPage(w, http.StatusBadRequest, v)
			return
		}
	} else {
		var done core.ActionCodeInfo
		if done, _, err = a.svc.ApplyActionCode(r.Context(), p, req.code, v.Lang, req.key); err == nil {
			info = done
		}
	}
	if err != nil {
		a.actionError(w, r, v, err)
		return
	}
	v.Message = v.text(key+"Done", subject(info))
	v.Continue = info.ContinueURL
	writeActionPage(w, http.StatusOK, v)
}
