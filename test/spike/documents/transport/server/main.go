// Command server is the Document Database's transport and auth spike (F0, ADR-027 and the
// ADR-010 amendment): a throwaway server, never part of Aruma's binary, that serves a fake
// Firestore over gRPC on a plain net/http server, the way Aruma's single port would, with
// requests routed by content-type (application/grpc*) and everything else (REST under /v1/, a
// token endpoint, /healthz) on an ordinary ServeMux.
//
// It serves the same fake through both candidate stacks, each on its own listeners, h2c and TLS:
//
//	-h2c-connect  127.0.0.1:9100   connectrpc.com/connect handlers
//	-h2c-grpcgo   127.0.0.1:9101   grpc.Server.ServeHTTP
//	-tls-connect  127.0.0.1:9102   the same over TLS, with a certificate from a CA made at start
//	-tls-grpcgo   127.0.0.1:9103   (written to -dir/ca.pem; for localhost, 127.0.0.1, 10.0.2.2)
//	-guard-proxy  127.0.0.1:9109   refuses and records whatever a client sends through it
//
// The servers take Aruma's timeouts (internal/server.Run), so a stream that outlives them shows
// whether the gRPC route clears them. Every request is recorded in -dir/record.jsonl: its
// headers, and the Authorization header by shape alone (see record.go). -dir also gets the fake
// service account's key files (sa-google.json, sa-aruma.json), made on first start.
//
//	go run ./test/spike/documents/transport/server -dir test/spike/documents/transport/.generated
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	var (
		dir           = flag.String("dir", ".generated", "where the record, the CA and the fake service account go")
		h2cConnect    = flag.String("h2c-connect", "127.0.0.1:9100", "connect over h2c (empty: off)")
		h2cGRPC       = flag.String("h2c-grpcgo", "127.0.0.1:9101", "grpc-go over h2c (empty: off)")
		tlsConnect    = flag.String("tls-connect", "127.0.0.1:9102", "connect over TLS (empty: off)")
		tlsGRPC       = flag.String("tls-grpcgo", "127.0.0.1:9103", "grpc-go over TLS (empty: off)")
		guard         = flag.String("guard-proxy", "127.0.0.1:9109", "the refusing proxy (empty: off)")
		later         = flag.Duration("later", 65*time.Second, "when Listen sends its later document")
		keepDeadlines = flag.Bool("keep-deadlines", false, "leave the server's read and write timeouts on gRPC streams")
	)
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, config{
		dir: *dir, later: *later, keepDeadlines: *keepDeadlines, guard: *guard,
		listeners: []listener{
			{"h2c-connect", *h2cConnect, false, "connect"},
			{"h2c-grpcgo", *h2cGRPC, false, "grpcgo"},
			{"tls-connect", *tlsConnect, true, "connect"},
			{"tls-grpcgo", *tlsGRPC, true, "grpcgo"},
		},
	}); err != nil {
		log.Error("spike server", "error", err)
		os.Exit(1)
	}
}

type config struct {
	dir           string
	later         time.Duration
	keepDeadlines bool
	guard         string
	listeners     []listener
}

type listener struct {
	name, addr string
	tls        bool
	stack      string // "connect" or "grpcgo"
}

func run(log *slog.Logger, cfg config) error {
	if err := os.MkdirAll(cfg.dir, 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(cfg.dir, "record.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	tokenURL := ""
	for _, l := range cfg.listeners {
		if l.tls && l.addr != "" && tokenURL == "" {
			tokenURL = "https://" + l.addr + "/oauth2/v1/token"
		}
	}
	sa, err := loadOrMakeSA(filepath.Join(cfg.dir, "sa"), tokenURL)
	if err != nil {
		return err
	}
	cert, err := makeCA(cfg.dir)
	if err != nil {
		return err
	}
	rec := &recorder{out: out, log: log, sa: sa}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := newSpike(log, rec, cfg.later, cfg.keepDeadlines)

	var servers []*http.Server
	errc := make(chan error, len(cfg.listeners)+1)
	for _, l := range cfg.listeners {
		if l.addr == "" {
			continue
		}
		srv := s.server(l, cert)
		ln, err := net.Listen("tcp", l.addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", l.addr, err)
		}
		log.Info("listening", "listener", l.name, "addr", ln.Addr().String())
		servers = append(servers, srv)
		go func() {
			var err error
			if l.tls {
				err = srv.ServeTLS(ln, "", "")
			} else {
				err = srv.Serve(ln)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	if cfg.guard != "" {
		proxy := &http.Server{Addr: cfg.guard, Handler: rec.guardProxy(), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, proxy)
		go func() {
			if err := proxy.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	return errors.Join(err, s.shutdown(servers, 15*time.Second))
}

// spike holds what every listener shares.
type spike struct {
	log           *slog.Logger
	rec           *recorder
	fake          *fake
	stopDrain     context.CancelFunc
	connect       http.Handler
	grpcgo        http.Handler
	keepDeadlines bool
}

func newSpike(log *slog.Logger, rec *recorder, later time.Duration, keepDeadlines bool) *spike {
	drain, stopDrain := context.WithCancel(context.Background())
	f := &fake{later: later, drain: drain}
	return &spike{
		log: log, rec: rec, fake: f, stopDrain: stopDrain, keepDeadlines: keepDeadlines,
		connect: newConnect(f, log),
		grpcgo:  newGRPC(f, log),
	}
}

// server is one listener's http.Server, with Aruma's timeouts and limits.
func (s *spike) server(l listener, cert tls.Certificate) *http.Server {
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	if l.tls {
		protocols.SetHTTP2(true)
	} else {
		protocols.SetUnencryptedHTTP2(true) // h2c with prior knowledge, which every gRPC client uses
	}
	srv := &http.Server{
		Addr:              l.addr,
		Handler:           s.handler(l),
		Protocols:         &protocols,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	if l.tls {
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	return srv
}

// handler routes by content-type first: gRPC to the stack, the rest to an ordinary mux. A path
// prefix cannot tell them apart, since the web SDK's WebChannel URLs sit under the gRPC service's
// path (hidane ADR 0003).
func (s *spike) handler(l listener) http.Handler {
	rpc := s.connect
	if l.stack == "grpcgo" {
		rpc = s.grpcgo
	}
	rpc = s.grpcRoute(rpc)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.Handle("POST /oauth2/v1/token", s.rec.tokenEndpoint(l.name))
	mux.Handle("/v1/", http.MaxBytesHandler(s.fake.rest(), maxMessage))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.rec.request(l.name, r)
		if isGRPC(r) {
			rpc.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// isGRPC reports whether a request speaks gRPC: application/grpc, or application/grpc+proto and
// the like. gRPC-Web (application/grpc-web*) is not: no Firestore client sends it.
func isGRPC(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	rest, ok := strings.CutPrefix(ct, "application/grpc")
	return ok && (rest == "" || rest[0] == '+' || rest[0] == ';')
}

// grpcRoute is the gRPC route's own middleware. It must not wrap the ResponseWriter in a type
// without Flush (both stacks assert http.Flusher on it), and it lifts the server's read and
// write timeouts, which net/http applies to each HTTP/2 stream: Aruma's 60 s would reset every
// Listen stream a minute after it opened.
func (s *spike) grpcRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.keepDeadlines {
			rc := http.NewResponseController(w)
			if err := errors.Join(rc.SetReadDeadline(time.Time{}), rc.SetWriteDeadline(time.Time{})); err != nil {
				s.log.Warn("cannot lift the stream's deadlines", "error", err)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// shutdown ends the open streams with UNAVAILABLE (the drain), then shuts the servers down.
// http.Server.Shutdown alone would wait for every Listen stream until its deadline.
func (s *spike) shutdown(servers []*http.Server, timeout time.Duration) error {
	s.stopDrain()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Go(func() {
			if err := srv.Shutdown(ctx); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("shutdown %s: %w", srv.Addr, err))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
