// Package smtptest runs a minimal SMTP server for tests: enough of RFC 5321 for net/smtp
// (EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA, QUIT), recording what it receives.
package smtptest

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Message is one received message.
type Message struct {
	From string
	To   []string
	Auth string // "user:password" from AUTH PLAIN, if any
	TLS  bool   // the session had switched to TLS
	Data string // the message as sent, dot-unstuffed, CRLF line ends
}

// Server is a running test server.
type Server struct {
	Host string
	Port int

	tls  *tls.Config
	ln   net.Listener
	mu   sync.Mutex
	msgs []Message
	fail int
}

// Start listens on a loopback port until the test ends. With tlsConfig set the server offers
// STARTTLS.
func Start(t testing.TB, tlsConfig *tls.Config) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	s := &Server{Host: "127.0.0.1", Port: addr.Port, tls: tlsConfig, ln: ln}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

// FailNext makes the next n DATA commands answer a temporary failure.
func (s *Server) FailNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = n
}

// Messages returns what the server has received so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.session(c)
	}
}

func (s *Server) session(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReader(c)
	reply := func(lines ...string) { _, _ = c.Write([]byte(strings.Join(lines, "\r\n") + "\r\n")) }
	reply("220 smtptest ESMTP")
	var m Message
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			ext := []string{"250-smtptest"}
			if s.tls != nil && !m.TLS {
				ext = append(ext, "250-STARTTLS")
			}
			reply(append(ext, "250-AUTH PLAIN", "250 8BITMIME")...)
		case "STARTTLS":
			if s.tls == nil || m.TLS {
				reply("502 not available")
				continue
			}
			reply("220 go ahead")
			tc := tls.Server(c, s.tls)
			if tc.Handshake() != nil {
				return
			}
			c, r, m.TLS = tc, bufio.NewReader(tc), true
		case "AUTH":
			mech, resp, _ := strings.Cut(arg, " ")
			if !strings.EqualFold(mech, "PLAIN") {
				reply("504 unsupported")
				continue
			}
			if resp == "" {
				reply("334 ")
				l, _ := r.ReadString('\n')
				resp = strings.TrimSpace(l)
			}
			b, err := base64.StdEncoding.DecodeString(resp)
			parts := strings.Split(string(b), "\x00")
			if err != nil || len(parts) != 3 {
				reply("535 bad credentials")
				continue
			}
			m.Auth = parts[1] + ":" + parts[2]
			reply("235 ok")
		case "MAIL":
			m.From, m.To = addr(arg), nil
			reply("250 ok")
		case "RCPT":
			m.To = append(m.To, addr(arg))
			reply("250 ok")
		case "DATA":
			s.mu.Lock()
			failing := s.fail > 0
			if failing {
				s.fail--
			}
			s.mu.Unlock()
			if failing {
				reply("451 try again later")
				continue
			}
			reply("354 send it")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			m.Data = b.String()
			s.mu.Lock()
			s.msgs = append(s.msgs, m)
			s.mu.Unlock()
			reply("250 queued")
		case "RSET", "NOOP":
			reply("250 ok")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("500 unknown command")
		}
	}
}

func addr(arg string) string {
	if i, j := strings.Index(arg, "<"), strings.Index(arg, ">"); i >= 0 && j > i {
		return arg[i+1 : j]
	}
	return arg
}
