// Package documents runs the official Go client of the Document Database,
// cloud.google.com/go/firestore, against a live Aruma (F1 of v0.4, plan §6): the client from
// firestore.NewClient for what it reads with the F1 RPCs (BatchGetDocuments, ListDocuments,
// ListCollectionIds) and for its writes, which all go through Commit (F2, so they are listed in
// test/compat/documents/expected-failures/admin-go.txt), and the generated client
// (cloud.google.com/go/firestore/apiv1) for GetDocument, CreateDocument, UpdateDocument and
// DeleteDocument. Every value of the production recording values-order.json is written and read
// back exactly.
//
// The client is configured as a developer would point it at Aruma: option.WithEndpoint with the
// server's host:port, gRPC without TLS (h2c, ADR-027 §3.3), and the service-account key file
// Aruma issued (option.WithAuthCredentialsFile). The client sends a self-signed JWT on every RPC
// (ADR-029); the server accepts it over plaintext because it is a loopback development server.
//
// TestMain builds test/smoke/genconfig and starts it with -serve -documents on a throwaway
// database that genconfig drops when it stops (Go and the local Postgres are needed). Every dial
// through http.DefaultTransport and the gRPC dialer is refused unless it is loopback, and the GCE
// metadata server is a stand-in that fails the run if anything asks it.
//
//	ARUMA_SMOKE_ADDR       Aruma's listen address (default 127.0.0.1:0: any free port)
//	ARUMA_SMOKE_DB_PREFIX  prefix of the throwaway database (default aruma_smoke_)
//
// ARUMA_SMOKE_DIR with ARUMA_SMOKE_DOCUMENTS_ADDR uses a server already running in that form
// (genconfig -serve -documents -out $ARUMA_SMOKE_DIR) and starts nothing.
package documents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var (
	live struct {
		dir, addr string
		expected  struct {
			ServerURL string `json:"serverUrl"`
			ProjectID string `json:"projectId"`
		}
	}
	keyFile string

	guardMu      sync.Mutex
	remotes      []string
	metadataHits atomic.Int64
)

// guardDial connects to loopback addresses alone; anything else is recorded and refused by
// name, before any lookup.
func guardDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		guardMu.Lock()
		remotes = append(remotes, addr)
		guardMu.Unlock()
		return nil, fmt.Errorf("network access beyond loopback blocked by the test: %s", addr)
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
}

// standInMetadata listens for cloud.google.com/go/compute/metadata, which dials through a
// transport of its own: GCE_METADATA_HOST sends it here, where every connection is counted and
// closed. With a key file nothing should ask.
func standInMetadata() (func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			metadataHits.Add(1)
			_ = c.Close()
		}
	}()
	if err := os.Setenv("GCE_METADATA_HOST", ln.Addr().String()); err != nil {
		return nil, err
	}
	return func() { _ = ln.Close() }, nil
}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if err := loadExpected(); err != nil {
		fmt.Fprintln(os.Stderr, "expected-failure list:", err)
		return 1
	}
	stopMetadata, err := standInMetadata()
	if err != nil {
		fmt.Fprintln(os.Stderr, "metadata stand-in:", err)
		return 1
	}
	defer stopMetadata()
	http.DefaultTransport.(*http.Transport).DialContext = guardDial
	for _, name := range []string{"FIRESTORE_EMULATOR_HOST", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "ARUMA_URL"} {
		_ = os.Unsetenv(name)
	}

	stop, err := startServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start Aruma:", err)
		return 1
	}
	defer stop()
	b, err := os.ReadFile(filepath.Join(live.dir, "expected.json"))
	if err == nil {
		err = json.Unmarshal(b, &live.expected)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "expected.json:", err)
		return 1
	}
	keyFile = filepath.Join(live.dir, "service-account.json")

	code := m.Run()
	if stale := checkStale(); len(stale) > 0 {
		fmt.Fprintf(os.Stderr, "%s names tests that did not run: %v\n", listFile, stale)
		code = 1
	}
	guardMu.Lock()
	if len(remotes) > 0 {
		fmt.Fprintln(os.Stderr, "network access beyond loopback attempted:", remotes)
		code = 1
	}
	guardMu.Unlock()
	if n := metadataHits.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "the GCE metadata server was asked %d times\n", n)
		code = 1
	}
	return code
}

// startServer starts genconfig -serve -documents, or uses the server ARUMA_SMOKE_DIR and
// ARUMA_SMOKE_DOCUMENTS_ADDR name.
func startServer() (func(), error) {
	if dir, addr := os.Getenv("ARUMA_SMOKE_DIR"), os.Getenv("ARUMA_SMOKE_DOCUMENTS_ADDR"); dir != "" && addr != "" {
		live.dir, live.addr = dir, addr
		return func() {}, nil
	}
	root := filepath.Join("..", "..", "..", "..")
	tmp, err := os.MkdirTemp("", "aruma-smoke-documents-")
	if err != nil {
		return nil, err
	}
	bin := filepath.Join(tmp, "genconfig")
	build := exec.Command("go", "build", "-o", bin, "./test/smoke/genconfig")
	build.Dir, build.Stdout, build.Stderr = root, os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("build genconfig: %w", err)
	}
	live.dir = filepath.Join(tmp, "out")
	cmd := exec.Command(bin, "-out", live.dir, "-serve", "-documents",
		"-addr", env("ARUMA_SMOKE_ADDR", "127.0.0.1:0"), "-db-prefix", env("ARUMA_SMOKE_DB_PREFIX", "aruma_smoke_"))
	cmd.Dir, cmd.Stderr = root, os.Stderr
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	stop := func() {
		_ = cmd.Process.Signal(syscall.SIGTERM) // genconfig drops its database on the way out
		_ = cmd.Wait()
		_ = os.RemoveAll(tmp)
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "export ARUMA_URL=http://"); ok {
			live.addr = v
		}
		if strings.HasPrefix(sc.Text(), "export ARUMA_CLI_TOKEN=") {
			break
		}
	}
	if live.addr == "" {
		stop()
		return nil, errors.New("genconfig printed no ARUMA_URL")
	}
	return stop, nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
