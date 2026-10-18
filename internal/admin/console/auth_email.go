package console

import (
	"net/http"
	"strconv"
	"time"

	identity "github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/httpx"
)

// authEmailRoutes registers the email templates, SMTP and dev mailbox routes.
func (a *API) authEmailRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {
	const base = "/console/v1/projects/{project}/auth"
	with(anyCred, "GET", base+"/templates", a.listMailTemplates)
	with(anyCred, "PUT", base+"/templates/{type}/{language}", a.setMailTemplate)
	with(anyCred, "DELETE", base+"/templates/{type}/{language}", a.resetMailTemplate)
	with(anyCred, "GET", base+"/smtp", a.getSMTP)
	with(anyCred, "PATCH", base+"/smtp", a.updateSMTP)
	with(anyCred, "POST", base+"/smtp:test", a.testSMTP)
	with(anyCred, "GET", base+"/mailbox", a.getMailbox)
	with(anyCred, "DELETE", base+"/mailbox", a.clearMailbox)
	with(anyCred, "POST", base+"/users/{uid}:sendPasswordReset", a.sendPasswordReset)
}

type mailTemplateJSON struct {
	Type       string `json:"type"`
	Language   string `json:"language"`
	Subject    string `json:"subject"`
	Body       string `json:"body"`
	Customized bool   `json:"customized"`
}

func toMailTemplateJSON(t identity.MailTemplate) mailTemplateJSON {
	return mailTemplateJSON{Type: t.Kind, Language: t.Language, Subject: t.Subject, Body: t.Body, Customized: t.Customized}
}

func (a *API) listMailTemplates(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ts, err := a.opts.Auth.MailTemplates(r.Context(), p)
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	out := make([]mailTemplateJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, toMailTemplateJSON(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"templates": out})
}

func (a *API) setMailTemplate(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	t, err := a.opts.Auth.SetMailTemplate(r.Context(), p, r.PathValue("type"), r.PathValue("language"), in.Subject, in.Body)
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toMailTemplateJSON(t))
}

// resetMailTemplate deletes the customisation and answers the built-in template.
func (a *API) resetMailTemplate(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	t, err := a.opts.Auth.ResetMailTemplate(r.Context(), p, r.PathValue("type"), r.PathValue("language"))
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toMailTemplateJSON(t))
}

type smtpJSON struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	PasswordSet   bool   `json:"passwordSet"`
	TLS           string `json:"tls"`
	SenderAddress string `json:"senderAddress"`
	SenderName    string `json:"senderName"`
	ReplyTo       string `json:"replyTo"`
	DevMailbox    bool   `json:"devMailbox"`
}

func toSMTPJSON(s identity.SMTPSettings) smtpJSON {
	return smtpJSON{
		Host: s.Host, Port: s.Port, Username: s.Username, PasswordSet: s.PasswordSet, TLS: s.TLS,
		SenderAddress: s.SenderAddress, SenderName: s.SenderName, ReplyTo: s.ReplyTo, DevMailbox: s.DevMailbox,
	}
}

func (a *API) getSMTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	s, err := a.opts.Auth.SMTP(r.Context(), p)
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toSMTPJSON(s))
}

// updateSMTP changes any of the settings. password is write-only: it is never returned, and an
// empty string removes it. passwordSet is read-only and ignored, so the screen can send back
// what it read.
func (a *API) updateSMTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Host          *string `json:"host"`
		Port          *int    `json:"port"`
		Username      *string `json:"username"`
		Password      *string `json:"password"`
		PasswordSet   *bool   `json:"passwordSet"`
		TLS           *string `json:"tls"`
		SenderAddress *string `json:"senderAddress"`
		SenderName    *string `json:"senderName"`
		ReplyTo       *string `json:"replyTo"`
		DevMailbox    *bool   `json:"devMailbox"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	s, err := a.opts.Auth.UpdateSMTP(r.Context(), p, identity.SMTPUpdate{
		Host: in.Host, Port: in.Port, Username: in.Username, Password: in.Password, TLS: in.TLS,
		SenderAddress: in.SenderAddress, SenderName: in.SenderName, ReplyTo: in.ReplyTo, DevMailbox: in.DevMailbox,
	})
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toSMTPJSON(s))
}

func (a *API) testSMTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	dev, err := a.opts.Auth.SendTestMail(r.Context(), p, in.To)
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"devMailbox": dev})
}

type mailboxMessageJSON struct {
	ID         string    `json:"id"`
	To         string    `json:"to"`
	Type       string    `json:"type"`
	Language   string    `json:"language"`
	Subject    string    `json:"subject"`
	Text       string    `json:"text"`
	HTML       string    `json:"html"`
	Link       string    `json:"link"`
	CreateTime time.Time `json:"createTime"`
}

// errNoMailbox answers the mailbox routes on an instance that does not allow the dev mailbox.
var errNoMailbox = apierr.New(apierr.NotFound, "The dev mailbox is off on this instance (mail.dev_mailbox).")

func (a *API) getMailbox(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.opts.Auth.DevMailboxAllowed() {
		a.fail(w, r, errNoMailbox)
		return
	}
	enabled, msgs, err := a.opts.Auth.Mailbox(r.Context(), p)
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	out := make([]mailboxMessageJSON, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, mailboxMessageJSON{
			ID: strconv.FormatInt(m.ID, 10), To: m.To, Type: m.Kind, Language: m.Language, Subject: m.Subject,
			Text: m.Text, HTML: m.HTML, Link: m.Link, CreateTime: m.Created,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": enabled, "messages": out})
}

func (a *API) clearMailbox(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.opts.Auth.DevMailboxAllowed() {
		a.fail(w, r, errNoMailbox)
		return
	}
	if err := a.opts.Auth.ClearMailbox(r.Context(), p); err != nil {
		a.failAuth(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sendPasswordReset emails the user a reset link and returns it. The optional language is
// "ar" (the default) or "en".
func (a *API) sendPasswordReset(w http.ResponseWriter, r *http.Request) {
	p, err := a.authProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Language string `json:"language"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	link, err := a.opts.Auth.SendPasswordResetEmail(r.Context(), p, r.PathValue("uid"), in.Language)
	if err != nil {
		a.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"link": link})
}
