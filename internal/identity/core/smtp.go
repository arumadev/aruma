package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/osama300/aurma-project/internal/platform/crypto"
)

// smtpTimeout bounds one whole SMTP conversation.
const smtpTimeout = 60 * time.Second

// smtpServer is a project's outgoing server.
type smtpServer struct {
	Host     string
	Port     int
	Username string
	Password string
	TLS      string // starttls, tls or none
}

// outgoing is one message to send.
type outgoing struct {
	From    mail.Address
	To      string
	ReplyTo string
	Subject string
	Text    string
	HTML    string
}

// send delivers a message. With "starttls" the server must offer STARTTLS: Aruma never falls
// back to plain text. Credentials are only sent over TLS (or to a loopback server), which
// net/smtp's PLAIN mechanism enforces.
func (m *mailer) send(ctx context.Context, srv smtpServer, helo string, msg outgoing) error {
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	addr := net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port))
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	tlsConfig := &tls.Config{ServerName: srv.Host, RootCAs: m.roots, MinVersion: tls.VersionTLS12}
	if srv.TLS == "tls" {
		conn = tls.Client(conn, tlsConfig)
	}
	c, err := smtp.NewClient(conn, srv.Host)
	if err != nil {
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()
	if err := c.Hello(helo); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}
	if srv.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the SMTP server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if srv.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", srv.Username, srv.Password, srv.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	data, err := msg.build()
	if err != nil {
		return err
	}
	if err := c.Mail(msg.From.Address); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(msg.To); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	return c.Quit()
}

// build renders the message as MIME: multipart/alternative with a plain-text and an HTML
// part, both UTF-8 and quoted-printable. Header values come from validated settings and from
// a subject with its line breaks removed, and are encoded, so none can add a header.
func (msg outgoing) build() ([]byte, error) {
	var b bytes.Buffer
	body := multipart.NewWriter(&b)
	domain := msg.From.Address[strings.LastIndex(msg.From.Address, "@")+1:]
	h := []string{
		"From: " + msg.From.String(),
		"To: " + (&mail.Address{Address: msg.To}).String(),
		"Subject: " + mime.QEncoding.Encode("utf-8", strings.Join(strings.Fields(msg.Subject), " ")),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: <" + crypto.RandomHex(16) + "@" + domain + ">",
		"MIME-Version: 1.0",
		"Auto-Submitted: auto-generated",
		"Content-Type: multipart/alternative; boundary=" + body.Boundary(),
	}
	if msg.ReplyTo != "" {
		h = append(h, "Reply-To: "+(&mail.Address{Address: msg.ReplyTo}).String())
	}
	var out bytes.Buffer
	out.WriteString(strings.Join(h, "\r\n") + "\r\n\r\n")
	for _, part := range []struct{ typ, content string }{{"text/plain", msg.Text}, {"text/html", msg.HTML}} {
		w, err := body.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(part.content)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := body.Close(); err != nil {
		return nil, err
	}
	out.Write(b.Bytes())
	return out.Bytes(), nil
}
