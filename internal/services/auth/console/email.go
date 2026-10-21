package console

import (
	"net/http"
	"strconv"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/auth/core"
)

// authEmailRoutes registers the email templates, SMTP and dev mailbox routes.
func (h *handlers) authEmailRoutes() {
	const base = "/console/v1/projects/{project}/auth"
	h.m.Handle(anyCred, "GET", base+"/templates", h.listMailTemplates)
	h.m.Handle(anyCred, "PUT", base+"/templates/{type}/{language}", h.setMailTemplate)
	h.m.Handle(anyCred, "DELETE", base+"/templates/{type}/{language}", h.resetMailTemplate)
	h.m.Handle(anyCred, "GET", base+"/smtp", h.getSMTP)
	h.m.Handle(anyCred, "PATCH", base+"/smtp", h.updateSMTP)
	h.m.Handle(anyCred, "POST", base+"/smtp:test", h.testSMTP)
	h.m.Handle(anyCred, "GET", base+"/mailbox", h.getMailbox)
	h.m.Handle(anyCred, "DELETE", base+"/mailbox", h.clearMailbox)
	h.m.Handle(anyCred, "POST", base+"/users/{uid}:sendPasswordReset", h.sendPasswordReset)
}

type mailTemplateJSON struct {
	Type       string `json:"type"`
	Language   string `json:"language"`
	Subject    string `json:"subject"`
	Body       string `json:"body"`
	Customized bool   `json:"customized"`
}

func toMailTemplateJSON(t core.MailTemplate) mailTemplateJSON {
	return mailTemplateJSON{Type: t.Kind, Language: t.Language, Subject: t.Subject, Body: t.Body, Customized: t.Customized}
}

func (h *handlers) listMailTemplates(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	ts, err := h.svc.MailTemplates(r.Context(), p)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	out := make([]mailTemplateJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, toMailTemplateJSON(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"templates": out})
}

func (h *handlers) setMailTemplate(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	t, err := h.svc.SetMailTemplate(r.Context(), p, r.PathValue("type"), r.PathValue("language"), in.Subject, in.Body)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toMailTemplateJSON(t))
}

// resetMailTemplate deletes the customisation and answers the built-in template.
func (h *handlers) resetMailTemplate(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	t, err := h.svc.ResetMailTemplate(r.Context(), p, r.PathValue("type"), r.PathValue("language"))
	if err != nil {
		h.failAuth(w, r, err)
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

func toSMTPJSON(s core.SMTPSettings) smtpJSON {
	return smtpJSON{
		Host: s.Host, Port: s.Port, Username: s.Username, PasswordSet: s.PasswordSet, TLS: s.TLS,
		SenderAddress: s.SenderAddress, SenderName: s.SenderName, ReplyTo: s.ReplyTo, DevMailbox: s.DevMailbox,
	}
}

func (h *handlers) getSMTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	s, err := h.svc.SMTP(r.Context(), p)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toSMTPJSON(s))
}

// updateSMTP changes any of the settings. password is write-only: it is never returned, and an
// empty string removes it. passwordSet is read-only and ignored, so the screen can send back
// what it read.
func (h *handlers) updateSMTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
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
		h.m.Fail(w, r, err)
		return
	}
	s, err := h.svc.UpdateSMTP(r.Context(), p, core.SMTPUpdate{
		Host: in.Host, Port: in.Port, Username: in.Username, Password: in.Password, TLS: in.TLS,
		SenderAddress: in.SenderAddress, SenderName: in.SenderName, ReplyTo: in.ReplyTo, DevMailbox: in.DevMailbox,
	})
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toSMTPJSON(s))
}

func (h *handlers) testSMTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	dev, err := h.svc.SendTestMail(r.Context(), p, in.To)
	if err != nil {
		h.failAuth(w, r, err)
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

func (h *handlers) getMailbox(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if !h.svc.DevMailboxAllowed() {
		h.m.Fail(w, r, errNoMailbox)
		return
	}
	enabled, msgs, err := h.svc.Mailbox(r.Context(), p)
	if err != nil {
		h.failAuth(w, r, err)
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

func (h *handlers) clearMailbox(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if !h.svc.DevMailboxAllowed() {
		h.m.Fail(w, r, errNoMailbox)
		return
	}
	if err := h.svc.ClearMailbox(r.Context(), p); err != nil {
		h.failAuth(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sendPasswordReset emails the user a reset link and returns it. The optional language is
// "ar" (the default) or "en".
func (h *handlers) sendPasswordReset(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in struct {
		Language string `json:"language"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	link, err := h.svc.SendPasswordResetEmail(r.Context(), p, r.PathValue("uid"), in.Language)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"link": link})
}
