package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/osama300/aurma-project/internal/platform/smtptest"
	"github.com/osama300/aurma-project/internal/services/auth/queries"
)

func TestRenderEscapesValues(t *testing.T) {
	tpl := MailTemplate{Subject: "Hi %DISPLAY_NAME%", Body: `<p>Hello %DISPLAY_NAME%</p><p><a href="%LINK%">open</a></p>`}
	m := tpl.render(mailVars{Link: "https://a.example/x?a=1&b=2", DisplayName: "<b>Eve</b>\r\nBcc: victim@example.com"})
	if strings.ContainsAny(m.Subject, "\r\n") || m.Subject != "Hi <b>Eve</b> Bcc: victim@example.com" {
		t.Fatalf("subject: %q", m.Subject)
	}
	if strings.Contains(m.HTML, "<b>Eve") || !strings.Contains(m.HTML, "&lt;b&gt;Eve&lt;/b&gt;") {
		t.Fatalf("display name not escaped in HTML: %s", m.HTML)
	}
	if !strings.Contains(m.HTML, `href="https://a.example/x?a=1&amp;b=2"`) {
		t.Fatalf("link not escaped as an attribute: %s", m.HTML)
	}
	if !strings.Contains(m.Text, "open: https://a.example/x?a=1&b=2") || !strings.Contains(m.Text, "Hello <b>Eve</b>") {
		t.Fatalf("text: %q", m.Text)
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText(`<div><p>One &amp; two</p><p><a href="%LINK%">%LINK%</a></p><p>Bye,<br>Team</p></div>`)
	want := "One & two\n%LINK%\nBye,\nTeam"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDefaultTemplates(t *testing.T) {
	for _, lang := range MailLanguages {
		for _, kind := range MailKinds {
			tpl := defaultTemplate(kind, lang)
			if err := validTemplate(tpl.Subject, tpl.Body); err != nil {
				t.Errorf("%s/%s: %v", kind, lang, err)
			}
			if lang == "ar" && !strings.Contains(tpl.Body, `dir="rtl"`) {
				t.Errorf("%s/ar is not right-to-left", kind)
			}
			if strings.Contains(strings.ToLower(tpl.Body), "firebase") || strings.Contains(tpl.Body, "Google") {
				t.Errorf("%s/%s names Google or Firebase", kind, lang)
			}
		}
	}
	if err := validTemplate("Subject", "<p>no link</p>"); err == nil {
		t.Error("a template without %LINK% was accepted")
	}
}

func TestMailLanguage(t *testing.T) {
	for in, want := range map[string]string{"": "ar", "ar": "ar", "ar-SA": "ar", "en": "en", "EN-us": "en", "en_GB": "en", "fr": "ar"} {
		if got := mailLanguage(in); got != want {
			t.Errorf("mailLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContinueURL(t *testing.T) {
	cfg := queries.AuthProjectConfig{AuthorizedDomains: []string{"localhost", "example.com", "10.0.0.5"}}
	for raw, want := range map[string]error{
		"":                             nil,
		"http://localhost:3000/done":   nil,
		"https://example.com/":         nil,
		"https://app.example.com/x":    nil,
		"https://EXAMPLE.com./x":       nil,
		"http://10.0.0.5/":             nil,
		"https://evil.example/":        ErrUnauthorizedDomain,
		"https://example.com.evil.io/": ErrUnauthorizedDomain,
		"https://notexample.com/":      ErrUnauthorizedDomain,
		"http://1.10.0.0.5/":           ErrUnauthorizedDomain,
		"/relative":                    ErrInvalidContinueURI,
		"javascript:alert(1)":          ErrInvalidContinueURI,
		"https://localhost@evil.io/":   ErrInvalidContinueURI,
		"ftp://example.com/":           ErrInvalidContinueURI,
	} {
		if got := checkContinueURL(cfg, raw); got != want {
			t.Errorf("checkContinueURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	for attempts, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 4: 4 * time.Minute, 8: time.Hour, 20: time.Hour} {
		if got := retryDelay(attempts); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", attempts, got, want)
		}
	}
}

// selfSigned returns a server certificate for 127.0.0.1 and a pool that trusts it.
func selfSigned(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtptest"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

func TestSendOverSTARTTLS(t *testing.T) {
	serverTLS, roots := selfSigned(t)
	srv := smtptest.Start(t, serverTLS)
	m := newMailer()
	m.roots = roots
	msg := outgoing{
		From: mail.Address{Name: "تطبيق الدردشة", Address: "noreply@chat.example"}, To: "sara@example.com",
		ReplyTo: "help@chat.example", Subject: "أكّد بريدك", Text: "افتح https://a.example/x?a=1&b=2", HTML: `<p dir="rtl">افتح</p>`,
	}
	err := m.send(context.Background(), smtpServer{Host: srv.Host, Port: srv.Port, Username: "mailer", Password: "s3cret", TLS: "starttls"}, "aruma.test", msg)
	if err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 || !got[0].TLS || got[0].Auth != "mailer:s3cret" || got[0].From != "noreply@chat.example" || got[0].To[0] != "sara@example.com" {
		t.Fatalf("received %+v", got)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(got[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	from, _ := parsed.Header.AddressList("From")
	if subject != "أكّد بريدك" || len(from) != 1 || from[0].Name != "تطبيق الدردشة" || parsed.Header.Get("Reply-To") != "<help@chat.example>" {
		t.Fatalf("headers: %v", parsed.Header)
	}
	_, params, _ := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	mr := multipart.NewReader(parsed.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p) // NextPart decodes quoted-printable
		parts = append(parts, p.Header.Get("Content-Type")+"|"+string(b))
	}
	if len(parts) != 2 || parts[0] != "text/plain; charset=utf-8|"+msg.Text || parts[1] != "text/html; charset=utf-8|"+msg.HTML {
		t.Fatalf("parts: %q", parts)
	}
}

func TestSendNeverDowngradesSTARTTLS(t *testing.T) {
	srv := smtptest.Start(t, nil) // offers no STARTTLS
	err := newMailer().send(context.Background(), smtpServer{Host: srv.Host, Port: srv.Port, TLS: "starttls"}, "aruma.test",
		outgoing{From: mail.Address{Address: "a@example.com"}, To: "b@example.com"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") || len(srv.Messages()) != 0 {
		t.Fatalf("err = %v, messages = %d", err, len(srv.Messages()))
	}
}

func TestSendRejectsUntrustedCertificate(t *testing.T) {
	serverTLS, _ := selfSigned(t)
	srv := smtptest.Start(t, serverTLS)
	err := newMailer().send(context.Background(), smtpServer{Host: srv.Host, Port: srv.Port, TLS: "starttls"}, "aruma.test",
		outgoing{From: mail.Address{Address: "a@example.com"}, To: "b@example.com"})
	if err == nil || len(srv.Messages()) != 0 {
		t.Fatalf("an untrusted certificate was accepted: %v", err)
	}
}
