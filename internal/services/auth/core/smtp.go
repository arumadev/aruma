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
	"net/netip"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/osama300/aurma-project/internal/platform/crypto"
)

// SMTP time limits. Connecting and the server's greeting are bounded tightly, so a port that
// accepts and stays silent fails fast instead of holding the console's request; the whole
// conversation ends well within the HTTP write timeout.
var (
	smtpDialTimeout     = 10 * time.Second
	smtpGreetingTimeout = 10 * time.Second
	smtpTimeout         = 30 * time.Second
)

// errPrivateSMTPHost refuses a connection to an address outside the public internet;
// ErrSMTPHostPrivate is how the console hears of it, so it can name the setting to change.
var (
	errPrivateSMTPHost = errors.New("the SMTP host is a loopback, private or link-local address, which this instance does not allow (mail.smtp_allow_private, ARUMA_MAIL_SMTP_ALLOW_PRIVATE)")
	ErrSMTPHostPrivate = fail("SMTP_HOST_PRIVATE")
)

// nonPublic are the ranges, beyond what netip classifies, that an SMTP host must not be in
// unless the instance allows private hosts: this network, shared address space (carrier NAT,
// and Alibaba Cloud's metadata at 100.100.100.200), IETF protocol assignments, benchmarking,
// reserved, and local-use NAT64.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// nat64 is the well-known NAT64 prefix, whose addresses stand for the IPv4 address they end in.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// publicAddr reports whether ip is a public unicast address: not loopback, private (RFC 1918,
// fc00::/7, which holds AWS's IPv6 metadata address), link-local (169.254.0.0/16, the metadata
// address of most clouds, and fe80::/10), unspecified, multicast or broadcast.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if nat64.Contains(ip) {
		b := ip.As16()
		return publicAddr(netip.AddrFrom4([4]byte(b[12:])))
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// privateHost reports whether a configured host is plainly not public: an address outside the
// public internet, or a localhost name. Other names are checked where they lead when a
// connection is made, so a name that later resolves elsewhere gains nothing.
func privateHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && !publicAddr(ip)
}

// refusePrivate is a dialer's Control: it sees the address actually being connected to, after
// name resolution, so DNS rebinding cannot slip a private address past the settings check.
func refusePrivate(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !publicAddr(ap.Addr()) {
		return errPrivateSMTPHost
	}
	return nil
}

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
	dialer := &net.Dialer{Timeout: smtpDialTimeout}
	if !m.allowPrivate {
		dialer.Control = refusePrivate
	}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if errors.Is(err, errPrivateSMTPHost) {
		return errPrivateSMTPHost
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer raw.Close()
	deadline, _ := ctx.Deadline()
	greeting := time.Now().Add(smtpGreetingTimeout)
	if deadline.Before(greeting) {
		greeting = deadline
	}
	_ = raw.SetDeadline(greeting) // the TLS handshake, for "tls", and the greeting
	conn := raw
	tlsConfig := &tls.Config{ServerName: srv.Host, RootCAs: m.roots, MinVersion: tls.VersionTLS12}
	if srv.TLS == "tls" {
		conn = tls.Client(raw, tlsConfig)
	}
	c, err := smtp.NewClient(conn, srv.Host)
	if err != nil {
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()
	_ = raw.SetDeadline(deadline)
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
