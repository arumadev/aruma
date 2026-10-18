package core

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"html"
	"net"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/identity/queries"
	"github.com/osama300/aurma-project/internal/platform/ratelimit"
	tcore "github.com/osama300/aurma-project/internal/tenancy/core"
)

// Auth email goes through auth.email_outbox: a message is written in the same transaction as
// the code its link carries, sealed, and a worker delivers it over the project's SMTP server
// with retries. In dev mailbox mode it is kept for the console instead and never sent.

// Flood limits on Auth email (research §3.7 and Firebase's sending quotas, docs/auth/limits).
// Firebase publishes daily quotas per project only; Aruma adds a per-recipient bucket so one
// address cannot be flooded, and a per-address bucket in the API layer.
const (
	recipientMailsPerHour = 5
	recipientMailBurst    = 5
	projectMailsPerHour   = 1000
	projectMailBurst      = 200
)

// Delivery retries back off from 30 s, doubling to at most an hour; after the last attempt the
// message is abandoned (about two hours after it was queued).
const (
	mailRetryBase   = 30 * time.Second
	mailRetryMax    = time.Hour
	mailMaxAttempts = 8
	mailBatch       = 20
	mailInterval    = 5 * time.Second
	mailboxMax      = 100
)

// MailOptions are the instance's email settings (config mail.*).
type MailOptions struct {
	// DevMailbox allows the dev mailbox: projects that have not chosen otherwise keep their
	// messages in the console instead of sending them, unless they have an SMTP host.
	DevMailbox bool
}

type mailer struct {
	devMailbox bool
	recipient  *ratelimit.Limiter // per project and recipient address
	project    *ratelimit.Limiter // per project
	roots      *x509.CertPool     // SMTP TLS roots; nil means the system's
}

func newMailer() *mailer {
	return &mailer{
		recipient: ratelimit.NewHourlyLimiter(recipientMailsPerHour, recipientMailBurst),
		project:   ratelimit.NewHourlyLimiter(projectMailsPerHour, projectMailBurst),
	}
}

// ConfigureMail applies the instance's email settings. Call it before serving.
func (s *Service) ConfigureMail(o MailOptions) { s.mail.devMailbox = o.DevMailbox }

// DevMailboxAllowed reports whether this instance offers the dev mailbox at all.
func (s *Service) DevMailboxAllowed() bool { return s.mail.devMailbox }

// devMailbox reports whether a project's messages go to the dev mailbox.
func (s *Service) devMailbox(cfg queries.AuthProjectConfig) bool {
	if !s.mail.devMailbox {
		return false
	}
	if cfg.MailDevMailbox != nil {
		return *cfg.MailDevMailbox
	}
	return cfg.SmtpHost == ""
}

// allowMail applies the per-recipient and per-project flood limits. Callers check the address
// before they look the user up, so a limited answer says nothing about the account.
func (s *Service) allowMail(p Project, recipient string, limited *Error) error {
	if ok, _ := s.mail.project.Allow(strconv.FormatInt(p.Number, 10)); !ok {
		return ErrTooManyAttempts
	}
	if ok, _ := s.mail.recipient.Allow(strconv.FormatInt(p.Number, 10) + "|" + strings.ToLower(recipient)); !ok {
		return limited
	}
	return nil
}

func outboxAAD(id int64) []byte {
	return []byte("auth.email_outbox|message|" + strconv.FormatInt(id, 10))
}

func smtpPasswordAAD(project int64) []byte {
	return []byte("auth.project_config|smtp_password_enc|" + strconv.FormatInt(project, 10))
}

// template returns the project's template of a kind and language, or the built-in one.
func (s *Service) template(ctx context.Context, q *queries.Queries, p Project, kind, lang string) (MailTemplate, error) {
	t, err := q.GetEmailTemplate(ctx, queries.GetEmailTemplateParams{ProjectNumber: p.Number, Kind: kind, Locale: lang})
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultTemplate(kind, lang), nil
	}
	if err != nil {
		return MailTemplate{}, err
	}
	return MailTemplate{Kind: kind, Language: lang, Subject: t.Subject, Body: t.Body, Customized: true}, nil
}

// enqueue renders a message and queues it in the caller's transaction.
func (s *Service) enqueue(ctx context.Context, q *queries.Queries, p Project, cfg queries.AuthProjectConfig, kind, lang, to string, v mailVars) error {
	t, err := s.template(ctx, q, p, kind, lang)
	if err != nil {
		return err
	}
	return s.queue(ctx, q, p, cfg, kind, lang, to, t.render(v))
}

func (s *Service) queue(ctx context.Context, q *queries.Queries, p Project, cfg queries.AuthProjectConfig, kind, lang, to string, m renderedMail) error {
	id, err := q.NextOutboxID(ctx)
	if err != nil {
		return err
	}
	plain, _ := json.Marshal(m)
	sealed, err := s.keys.Seal(plain, outboxAAD(id))
	if err != nil {
		return err
	}
	dev := s.devMailbox(cfg)
	var sent *time.Time
	if dev {
		now := s.now()
		sent = &now // kept for the console, which is its delivery
	}
	return q.InsertOutbox(ctx, queries.InsertOutboxParams{
		ID: id, ProjectNumber: p.Number, Kind: kind, Recipient: to, Locale: lang, MessageEnc: sealed, DevMailbox: dev, SentAt: sent,
	})
}

func (s *Service) openMessage(id int64, sealed []byte) (renderedMail, error) {
	var m renderedMail
	plain, err := s.keys.Open(sealed, outboxAAD(id))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(plain, &m)
	return m, err
}

// MailTemplates returns every kind in both languages, customised or built in.
func (s *Service) MailTemplates(ctx context.Context, p Project) ([]MailTemplate, error) {
	rows, err := s.q.ListEmailTemplates(ctx, p.Number)
	if err != nil {
		return nil, err
	}
	out := make([]MailTemplate, 0, len(MailKinds)*len(MailLanguages))
	for _, kind := range MailKinds {
		for _, lang := range MailLanguages {
			t := defaultTemplate(kind, lang)
			for _, r := range rows {
				if r.Kind == kind && r.Locale == lang {
					t.Subject, t.Body, t.Customized = r.Subject, r.Body, true
				}
			}
			out = append(out, t)
		}
	}
	return out, nil
}

func validKindLang(kind, lang string) error {
	if !slices.Contains(MailKinds, kind) {
		return failf("INVALID_TEMPLATE", "unknown template type "+kind)
	}
	if !slices.Contains(MailLanguages, lang) {
		return failf("INVALID_TEMPLATE", "unknown template language "+lang)
	}
	return nil
}

// SetMailTemplate customises a template.
func (s *Service) SetMailTemplate(ctx context.Context, p Project, kind, lang, subject, body string) (MailTemplate, error) {
	if err := validKindLang(kind, lang); err != nil {
		return MailTemplate{}, err
	}
	if err := validTemplate(subject, body); err != nil {
		return MailTemplate{}, err
	}
	if _, err := s.q.UpsertEmailTemplate(ctx, queries.UpsertEmailTemplateParams{ProjectNumber: p.Number, Kind: kind, Locale: lang, Subject: subject, Body: body}); err != nil {
		return MailTemplate{}, err
	}
	s.audit(ctx, s.q, p, "", "mail.template.update", "ok", map[string]any{"type": kind, "language": lang})
	return MailTemplate{Kind: kind, Language: lang, Subject: subject, Body: body, Customized: true}, nil
}

// ResetMailTemplate returns a template to Aruma's built-in text.
func (s *Service) ResetMailTemplate(ctx context.Context, p Project, kind, lang string) (MailTemplate, error) {
	if err := validKindLang(kind, lang); err != nil {
		return MailTemplate{}, err
	}
	if err := s.q.DeleteEmailTemplate(ctx, queries.DeleteEmailTemplateParams{ProjectNumber: p.Number, Kind: kind, Locale: lang}); err != nil {
		return MailTemplate{}, err
	}
	s.audit(ctx, s.q, p, "", "mail.template.reset", "ok", map[string]any{"type": kind, "language": lang})
	return defaultTemplate(kind, lang), nil
}

// SMTPSettings are a project's outgoing mail settings. The password is never returned.
type SMTPSettings struct {
	Host          string
	Port          int
	Username      string
	PasswordSet   bool
	TLS           string // starttls, tls or none
	SenderAddress string
	SenderName    string
	ReplyTo       string
	DevMailbox    bool // messages are kept in the dev mailbox instead of sent
}

// SMTPUpdate changes some SMTP settings; nil fields are left as they are. An empty Password
// clears it. DevMailbox pins the project's choice, which needs an instance that allows it.
type SMTPUpdate struct {
	Host, Username, Password, TLS, SenderAddress, SenderName, ReplyTo *string
	Port                                                              *int
	DevMailbox                                                        *bool
}

func (s *Service) toSMTPSettings(c queries.AuthProjectConfig) SMTPSettings {
	return SMTPSettings{
		Host: c.SmtpHost, Port: int(c.SmtpPort), Username: c.SmtpUsername, PasswordSet: len(c.SmtpPasswordEnc) > 0,
		TLS: c.SmtpTls, SenderAddress: c.MailSenderAddress, SenderName: c.MailSenderName, ReplyTo: c.MailReplyTo,
		DevMailbox: s.devMailbox(c),
	}
}

// SMTP returns the project's mail settings.
func (s *Service) SMTP(ctx context.Context, p Project) (SMTPSettings, error) {
	c, err := s.Config(ctx, p)
	if err != nil {
		return SMTPSettings{}, err
	}
	return s.toSMTPSettings(c), nil
}

// UpdateSMTP changes the project's mail settings. The password is sealed to its row.
func (s *Service) UpdateSMTP(ctx context.Context, p Project, u SMTPUpdate) (SMTPSettings, error) {
	c, err := s.Config(ctx, p)
	if err != nil {
		return SMTPSettings{}, err
	}
	bad := func(detail string) (SMTPSettings, error) { return SMTPSettings{}, failf("INVALID_SMTP_CONFIG", detail) }
	if u.Host != nil {
		h := strings.TrimSpace(*u.Host)
		if h != "" && !validHost(h) {
			return bad("host must be a host name or address")
		}
		c.SmtpHost = h
	}
	if u.Port != nil {
		if *u.Port < 1 || *u.Port > 65535 {
			return bad("port must be 1 to 65535")
		}
		c.SmtpPort = int32(*u.Port)
	}
	if u.TLS != nil {
		if !slices.Contains([]string{"starttls", "tls", "none"}, *u.TLS) {
			return bad("tls must be starttls, tls or none")
		}
		c.SmtpTls = *u.TLS
	}
	for _, f := range []struct {
		in    *string
		dst   *string
		email bool
		name  string
	}{
		{u.Username, &c.SmtpUsername, false, "username"},
		{u.SenderName, &c.MailSenderName, false, "senderName"},
		{u.SenderAddress, &c.MailSenderAddress, true, "senderAddress"},
		{u.ReplyTo, &c.MailReplyTo, true, "replyTo"},
	} {
		if f.in == nil {
			continue
		}
		v := strings.TrimSpace(*f.in)
		if strings.ContainsAny(v, "\r\n") || !utf8.ValidString(v) || len(v) > 254 {
			return bad(f.name + " is not valid")
		}
		if f.email && v != "" {
			if _, ok := normalizeEmail(v); !ok {
				return bad(f.name + " must be an email address")
			}
		}
		*f.dst = v
	}
	if u.Password != nil {
		switch {
		case *u.Password == "":
			c.SmtpPasswordEnc = nil
		case len(*u.Password) > 1024 || strings.ContainsAny(*u.Password, "\r\n"):
			return bad("password is not valid")
		default:
			if c.SmtpPasswordEnc, err = s.keys.Seal([]byte(*u.Password), smtpPasswordAAD(p.Number)); err != nil {
				return SMTPSettings{}, err
			}
		}
	}
	if u.DevMailbox != nil {
		if *u.DevMailbox && !s.mail.devMailbox {
			return SMTPSettings{}, failf("DEV_MAILBOX_DISABLED", "this instance does not allow the dev mailbox (mail.dev_mailbox)")
		}
		c.MailDevMailbox = u.DevMailbox
	}
	out, err := s.q.UpdateMailConfig(ctx, queries.UpdateMailConfigParams{
		ProjectNumber: p.Number, MailSenderName: c.MailSenderName, MailSenderAddress: c.MailSenderAddress,
		MailReplyTo: c.MailReplyTo, SmtpHost: c.SmtpHost, SmtpPort: c.SmtpPort, SmtpUsername: c.SmtpUsername,
		SmtpPasswordEnc: c.SmtpPasswordEnc, SmtpTls: c.SmtpTls, MailDevMailbox: c.MailDevMailbox,
	})
	if err != nil {
		return SMTPSettings{}, err
	}
	s.audit(ctx, s.q, p, "", "mail.smtp.update", "ok", map[string]any{"host": c.SmtpHost, "password": u.Password != nil})
	return s.toSMTPSettings(out), nil
}

func validHost(h string) bool {
	if len(h) > 253 {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	u, err := url.Parse("smtp://" + h)
	return err == nil && u.Host == h && u.Port() == "" && !strings.ContainsAny(h, " @/")
}

// MailboxMessage is a message kept in the dev mailbox.
type MailboxMessage struct {
	ID       int64
	To       string
	Kind     string
	Language string
	Subject  string
	Text     string
	HTML     string
	Link     string
	Created  time.Time
}

// Mailbox returns the newest messages of the project's dev mailbox, and whether the project
// currently sends to it.
func (s *Service) Mailbox(ctx context.Context, p Project) (bool, []MailboxMessage, error) {
	c, err := s.Config(ctx, p)
	if err != nil {
		return false, nil, err
	}
	rows, err := s.q.ListMailbox(ctx, queries.ListMailboxParams{ProjectNumber: p.Number, MaxMessages: mailboxMax})
	if err != nil {
		return false, nil, err
	}
	out := make([]MailboxMessage, 0, len(rows))
	for _, r := range rows {
		m, err := s.openMessage(r.ID, r.MessageEnc)
		if err != nil {
			s.log.Warn("dev mailbox: unreadable message", "id", r.ID, "error", err)
			continue
		}
		out = append(out, MailboxMessage{
			ID: r.ID, To: r.Recipient, Kind: r.Kind, Language: r.Locale, Subject: m.Subject, Text: m.Text,
			HTML: m.HTML, Link: m.Link, Created: r.CreatedAt,
		})
	}
	return s.devMailbox(c), out, nil
}

// ClearMailbox empties the project's dev mailbox.
func (s *Service) ClearMailbox(ctx context.Context, p Project) error {
	_, err := s.q.ClearMailbox(ctx, p.Number)
	return err
}

// SendTestMail sends a short message to check the project's settings: to the dev mailbox when
// the project uses it, otherwise straight over SMTP, so the console sees the server's answer.
func (s *Service) SendTestMail(ctx context.Context, p Project, to string) (devMailbox bool, err error) {
	addr, ok := normalizeEmail(to)
	if !ok {
		return false, ErrInvalidEmail
	}
	c, err := s.Config(ctx, p)
	if err != nil {
		return false, err
	}
	prof, err := s.control.ProjectProfile(ctx, p.Number)
	if err != nil {
		return false, err
	}
	app := appName(c, prof)
	ar, en := "إعدادات البريد في "+app+" تعمل.", "The mail settings of "+app+" work."
	m := renderedMail{
		Subject: "رسالة تجريبية من " + app + " / Test message from " + app,
		Text:    ar + "\n\n" + en,
		HTML: `<div dir="rtl" lang="ar"><p>` + html.EscapeString(ar) + `</p></div>` +
			`<div dir="ltr" lang="en"><p>` + html.EscapeString(en) + `</p></div>`,
	}
	if s.devMailbox(c) {
		return true, s.queue(ctx, s.q, p, c, "TEST", defaultMailLang, addr, m)
	}
	if err := s.deliver(ctx, c, prof, addr, m); err != nil {
		return false, failf("SMTP_ERROR", err.Error())
	}
	return false, nil
}

func appName(c queries.AuthProjectConfig, prof tcore.ProjectProfile) string {
	if c.AppName != "" {
		return c.AppName
	}
	return prof.DisplayName
}

// sender is the From address of a project's mail: as configured, else noreply at its domain.
func (s *Service) sender(c queries.AuthProjectConfig, prof tcore.ProjectProfile) mail.Address {
	addr := c.MailSenderAddress
	if addr == "" {
		addr = "noreply@" + prof.AuthDomain
	}
	name := c.MailSenderName
	if name == "" {
		name = appName(c, prof)
	}
	return mail.Address{Name: name, Address: addr}
}

// deliver sends one message over the project's SMTP server.
func (s *Service) deliver(ctx context.Context, c queries.AuthProjectConfig, prof tcore.ProjectProfile, to string, m renderedMail) error {
	if c.SmtpHost == "" {
		return errors.New("no SMTP server is configured")
	}
	var password string
	if len(c.SmtpPasswordEnc) > 0 {
		b, err := s.keys.Open(c.SmtpPasswordEnc, smtpPasswordAAD(c.ProjectNumber))
		if err != nil {
			return errors.New("the SMTP password cannot be decrypted; set it again")
		}
		password = string(b)
	}
	msg := outgoing{From: s.sender(c, prof), To: to, ReplyTo: c.MailReplyTo, Subject: m.Subject, Text: m.Text, HTML: m.HTML}
	srv := smtpServer{Host: c.SmtpHost, Port: int(c.SmtpPort), Username: c.SmtpUsername, Password: password, TLS: c.SmtpTls}
	return s.mail.send(ctx, srv, s.helloName(), msg)
}

// helloName is the name Aruma greets SMTP servers with: the host of its public URL.
func (s *Service) helloName() string {
	if u, err := url.Parse(s.publicURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "localhost"
}

// DeliverMail sends the queued messages that are due and reports how many were sent. Failures
// are retried with backoff; the error is kept on the row, never the message.
func (s *Service) DeliverMail(ctx context.Context) (int, error) {
	rows, err := s.q.ClaimOutbox(ctx, mailBatch)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, r := range rows {
		err := s.deliverRow(ctx, r)
		if err == nil {
			sent++
			if err := s.q.MarkOutboxSent(ctx, queries.MarkOutboxSentParams{ProjectNumber: r.ProjectNumber, ID: r.ID}); err != nil {
				return sent, err
			}
			continue
		}
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		giveUp := r.Attempts >= mailMaxAttempts
		s.log.Warn("auth mail delivery failed", "project", r.ProjectNumber, "id", r.ID, "attempt", r.Attempts, "abandoned", giveUp, "error", msg)
		if err := s.q.MarkOutboxRetry(ctx, queries.MarkOutboxRetryParams{
			ProjectNumber: r.ProjectNumber, ID: r.ID, LastError: &msg, NextAttemptAt: s.now().Add(retryDelay(int(r.Attempts))), GiveUp: giveUp,
		}); err != nil {
			return sent, err
		}
	}
	return sent, nil
}

func (s *Service) deliverRow(ctx context.Context, r queries.ClaimOutboxRow) error {
	p := Project{Number: r.ProjectNumber}
	c, err := s.Config(ctx, p)
	if err != nil {
		return err
	}
	prof, err := s.control.ProjectProfile(ctx, p.Number)
	if err != nil {
		return errors.New("the project is not active")
	}
	m, err := s.openMessage(r.ID, r.MessageEnc)
	if err != nil {
		return errors.New("the message cannot be decrypted")
	}
	return s.deliver(ctx, c, prof, r.Recipient, m)
}

// retryDelay is the wait after the given number of failed attempts.
func retryDelay(attempts int) time.Duration {
	d := mailRetryBase
	for i := 1; i < attempts && d < mailRetryMax; i++ {
		d *= 2
	}
	return min(d, mailRetryMax)
}

// purgeMail removes spent codes and old messages.
func (s *Service) purgeMail(ctx context.Context) error {
	codes, err := s.q.PurgeOobCodes(ctx)
	if err != nil {
		return err
	}
	msgs, err := s.q.PurgeOutbox(ctx)
	if codes+msgs > 0 {
		s.log.Info("auth housekeeping", "oob_codes_purged", codes, "messages_purged", msgs)
	}
	return err
}

// RunMailer delivers queued mail until ctx is cancelled.
func (s *Service) RunMailer(ctx context.Context) {
	t := time.NewTicker(mailInterval)
	defer t.Stop()
	for {
		if _, err := s.DeliverMail(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("auth mail delivery", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
