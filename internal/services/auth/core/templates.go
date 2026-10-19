package core

import (
	"html"
	"regexp"
	"strings"
)

// The four email kinds and the languages every end-user email exists in (Arabic first).
const (
	MailVerifyEmail    = "VERIFY_EMAIL"
	MailPasswordReset  = "PASSWORD_RESET"
	MailChangeEmail    = "VERIFY_AND_CHANGE_EMAIL"
	MailRecoverEmail   = "RECOVER_EMAIL"
	defaultMailLang    = "ar"
	maxTemplateSubject = 200
	maxTemplateBody    = 64 << 10
)

var (
	// MailKinds lists the template kinds in the order the console shows them.
	MailKinds = []string{MailVerifyEmail, MailPasswordReset, MailChangeEmail, MailRecoverEmail}
	// MailLanguages lists the template languages.
	MailLanguages = []string{"ar", "en"}
)

// MailTemplate is an email template. Body is HTML; the variables %LINK%, %APP_NAME%, %EMAIL%,
// %NEW_EMAIL% and %DISPLAY_NAME% are replaced when a message is written, HTML-escaped in the
// body and as plain text in the subject. Customized is false for Aruma's built-in text.
type MailTemplate struct {
	Kind       string
	Language   string
	Subject    string
	Body       string
	Customized bool
}

// mailVars are the values of a template's variables.
type mailVars struct {
	Link, AppName, Email, NewEmail, DisplayName string
}

func (v mailVars) replacer(escape func(string) string) *strings.Replacer {
	return strings.NewReplacer(
		"%LINK%", escape(v.Link), "%APP_NAME%", escape(v.AppName), "%EMAIL%", escape(v.Email),
		"%NEW_EMAIL%", escape(v.NewEmail), "%DISPLAY_NAME%", escape(v.DisplayName),
	)
}

// renderedMail is a message ready to queue.
type renderedMail struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
	Link    string `json:"link"`
}

// render fills a template. The plain-text part is derived from the template before the values
// go in, so a value can never inject markup into either part; the subject loses any line
// break, so a value cannot add a header.
func (t MailTemplate) render(v mailVars) renderedMail {
	plain := v.replacer(func(s string) string { return s })
	subject := strings.Join(strings.Fields(plain.Replace(t.Subject)), " ")
	return renderedMail{
		Subject: subject,
		Text:    plain.Replace(htmlToText(t.Body)),
		HTML:    v.replacer(html.EscapeString).Replace(t.Body),
		Link:    v.Link,
	}
}

var (
	anchorRe    = regexp.MustCompile(`(?is)<a\s[^>]*?href\s*=\s*["']([^"']*)["'][^>]*>(.*?)</a>`)
	breakRe     = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|h[1-6]|li|tr|table)>`)
	tagRe       = regexp.MustCompile(`(?s)<[^>]*>`)
	blankRunsRe = regexp.MustCompile(`\n{3,}`)
)

// htmlToText turns a template body into its plain-text alternative: links keep their target,
// block ends become line breaks, other tags go.
func htmlToText(body string) string {
	body = anchorRe.ReplaceAllStringFunc(body, func(a string) string {
		m := anchorRe.FindStringSubmatch(a)
		href, text := html.UnescapeString(m[1]), strings.TrimSpace(tagRe.ReplaceAllString(m[2], ""))
		if text == "" || html.UnescapeString(text) == href {
			return href
		}
		return text + ": " + href
	})
	body = breakRe.ReplaceAllString(body, "\n")
	body = html.UnescapeString(tagRe.ReplaceAllString(body, ""))
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.TrimSpace(blankRunsRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// validTemplate checks a template the console saves: both parts present and bounded, and the
// link present, since a message without it cannot do its job.
func validTemplate(subject, body string) error {
	switch {
	case strings.TrimSpace(subject) == "" || strings.TrimSpace(body) == "":
		return failf("INVALID_TEMPLATE", "subject and body are required")
	case len([]rune(subject)) > maxTemplateSubject:
		return failf("INVALID_TEMPLATE", "the subject is longer than 200 characters")
	case len(body) > maxTemplateBody:
		return failf("INVALID_TEMPLATE", "the body is larger than 64 KiB")
	case !strings.Contains(body, "%LINK%"):
		return failf("INVALID_TEMPLATE", "the body must contain %LINK%")
	}
	return nil
}

// defaultTemplate returns Aruma's built-in template. The texts are Aruma's own (research §7):
// a greeting, the action in one sentence, the link, what to do if the mail was unexpected,
// and a sign-off from the app.
func defaultTemplate(kind, lang string) MailTemplate {
	t := defaultTemplates[lang][kind]
	return MailTemplate{Kind: kind, Language: lang, Subject: t[0], Body: t[1]}
}

func wrapAr(s string) string {
	return `<div dir="rtl" lang="ar" style="font-family:Tahoma,Arial,sans-serif;line-height:1.7">` + s + `</div>`
}

func wrapEn(s string) string {
	return `<div dir="ltr" lang="en" style="font-family:Arial,sans-serif;line-height:1.6">` + s + `</div>`
}

const linkPara = `<p><a href="%LINK%">%LINK%</a></p>`

var defaultTemplates = map[string]map[string][2]string{
	"ar": {
		MailVerifyEmail: {"أكّد بريدك الإلكتروني في %APP_NAME%", wrapAr(
			`<p>مرحبًا %DISPLAY_NAME%،</p>` +
				`<p>لتأكيد أن العنوان %EMAIL% يخصّك، افتح الرابط التالي:</p>` + linkPara +
				`<p>إن لم تطلب هذا فتجاهل الرسالة، ولن يتغيّر شيء في أي حساب.</p>` +
				`<p>مع التحية،<br>فريق %APP_NAME%</p>`)},
		MailPasswordReset: {"إعادة تعيين كلمة المرور في %APP_NAME%", wrapAr(
			`<p>مرحبًا %DISPLAY_NAME%،</p>` +
				`<p>وصلنا طلب لتعيين كلمة مرور جديدة لحسابك (%EMAIL%). اختر كلمة المرور الجديدة من هذا الرابط خلال ساعة:</p>` + linkPara +
				`<p>إن لم تطلب ذلك فتجاهل الرسالة، وستبقى كلمة مرورك الحالية كما هي.</p>` +
				`<p>مع التحية،<br>فريق %APP_NAME%</p>`)},
		MailChangeEmail: {"أكّد بريدك الجديد في %APP_NAME%", wrapAr(
			`<p>مرحبًا %DISPLAY_NAME%،</p>` +
				`<p>طُلب تغيير بريد حسابك من %EMAIL% إلى %NEW_EMAIL%. لإتمام التغيير، افتح الرابط التالي:</p>` + linkPara +
				`<p>إن لم تطلب هذا فتجاهل الرسالة، وسيبقى بريد الحساب كما هو.</p>` +
				`<p>مع التحية،<br>فريق %APP_NAME%</p>`)},
		MailRecoverEmail: {"تغيّر بريد حسابك في %APP_NAME%", wrapAr(
			`<p>مرحبًا %DISPLAY_NAME%،</p>` +
				`<p>تغيّر بريد الدخول إلى حسابك من %EMAIL% إلى %NEW_EMAIL%.</p>` +
				`<p>إن لم تكن أنت من غيّره، فأعد البريد السابق من هذا الرابط، ثم غيّر كلمة المرور:</p>` + linkPara +
				`<p>مع التحية،<br>فريق %APP_NAME%</p>`)},
	},
	"en": {
		MailVerifyEmail: {"Confirm your email for %APP_NAME%", wrapEn(
			`<p>Hello %DISPLAY_NAME%,</p>` +
				`<p>To confirm that %EMAIL% is yours, open this link:</p>` + linkPara +
				`<p>If you did not ask for this, ignore this message; no account changes.</p>` +
				`<p>Regards,<br>The %APP_NAME% team</p>`)},
		MailPasswordReset: {"Reset your %APP_NAME% password", wrapEn(
			`<p>Hello %DISPLAY_NAME%,</p>` +
				`<p>We received a request to set a new password for your account (%EMAIL%). Choose it from this link within an hour:</p>` + linkPara +
				`<p>If you did not ask for this, ignore this message; your current password stays as it is.</p>` +
				`<p>Regards,<br>The %APP_NAME% team</p>`)},
		MailChangeEmail: {"Confirm your new email for %APP_NAME%", wrapEn(
			`<p>Hello %DISPLAY_NAME%,</p>` +
				`<p>Someone asked to change your account email from %EMAIL% to %NEW_EMAIL%. To complete the change, open this link:</p>` + linkPara +
				`<p>If you did not ask for this, ignore this message; the account email stays as it is.</p>` +
				`<p>Regards,<br>The %APP_NAME% team</p>`)},
		MailRecoverEmail: {"Your %APP_NAME% email was changed", wrapEn(
			`<p>Hello %DISPLAY_NAME%,</p>` +
				`<p>The sign-in email of your account changed from %EMAIL% to %NEW_EMAIL%.</p>` +
				`<p>If you did not make this change, restore your previous email with this link, then change your password:</p>` + linkPara +
				`<p>Regards,<br>The %APP_NAME% team</p>`)},
	},
}

// mailLanguage picks ar or en from an X-Firebase-Locale value such as "en-US"; anything else
// is the project default, Arabic.
func mailLanguage(locale string) string {
	tag := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(tag, "-_,;"); i >= 0 {
		tag = tag[:i]
	}
	if tag == "en" {
		return "en"
	}
	return defaultMailLang
}
