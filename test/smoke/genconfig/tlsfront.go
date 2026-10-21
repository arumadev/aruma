package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

// tlsFront stands before Aruma as Caddy does in production (ADR-018): it terminates TLS for
// the database hosts and forwards to the plain http server, WebSocket upgrades included. The
// Host header passes through unchanged, so Aruma sees <ns>.<rtdb.domain>; X-Forwarded-For,
// -Host and -Proto are set as Caddy's reverse_proxy sets them.
type tlsFront struct {
	ln  net.Listener
	srv *http.Server
}

// startTLSFront listens on addr with a certificate for <projectID>-default-rtdb.<domain> and
// *.<domain>, signed by a CA made for this run and valid for a day. The CA certificate goes
// to caFile, for the clients to trust; its key never leaves this process.
func startTLSFront(addr, domain, target, caFile string) (*tlsFront, error) {
	cert, caPEM, err := issue(domain)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(caFile, caPEM, 0o644); err != nil {
		return nil, err
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	f := &tlsFront{ln: ln, srv: &http.Server{
		Handler:   proxy,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ErrorLog:  log.New(io.Discard, "", 0),
	}}
	go func() { _ = f.srv.ServeTLS(ln, "", "") }()
	return f, nil
}

// Addr is the address the front listens on.
func (f *tlsFront) Addr() string { return f.ln.Addr().String() }

// Close stops the front. Upgraded connections are left to the process's exit.
func (f *tlsFront) Close() { _ = f.srv.Close() }

// issue makes the run's CA and the server certificate it signs.
func issue(domain string) (tls.Certificate, []byte, error) {
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Aruma smoke test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	if ca, err = x509.ParseCertificate(caDER); err != nil {
		return tls.Certificate{}, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	host := projectID + "-default-rtdb." + domain
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host, "*." + domain},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, caPEM, nil
}
