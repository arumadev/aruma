// Package rtdbkit runs a real Aruma binary with the Realtime Database enabled, on a
// throwaway Postgres database, and talks to it as a client would: over the wire protocol
// (client.go), long polling (lp.go) and REST (rest.go). The chaos tool (tools/rtdb-chaos)
// kills and restarts the server it starts; the bench (tools/bench) measures it.
package rtdbkit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultAdminURL is the local Postgres the tools create their databases on, unless
// ARUMA_TEST_DATABASE_URL names another.
const DefaultAdminURL = "postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable"

// AdminURL is the Postgres URL databases are created and dropped through.
func AdminURL() string {
	if u := os.Getenv("ARUMA_TEST_DATABASE_URL"); u != "" {
		return u
	}
	return DefaultAdminURL
}

var validPrefix = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}_$`)

// DBPrefix starts the name of every database the tools create: ARUMA_TEST_DB_PREFIX when
// it is valid, as for the tests (internal/store/storetest), else "aruma_".
func DBPrefix() string {
	if p := os.Getenv("ARUMA_TEST_DB_PREFIX"); validPrefix.MatchString(p) {
		return p
	}
	return "aruma_"
}

// RandomHex returns n random bytes in hex.
func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Build compiles cmd/aruma from the module that contains the working directory into dir and
// returns the binary's path.
func Build(ctx context.Context, dir string) (string, error) {
	root, err := ModuleRoot()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "aruma")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/aruma")
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build aruma: %w", err)
	}
	return bin, nil
}

// ModuleRoot is the directory of the go.mod above the working directory.
func ModuleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		return "", errors.New("run from inside the Aruma module")
	}
	return filepath.Dir(mod), nil
}

// Options configure a server.
type Options struct {
	Bin  string // the aruma binary
	Port int    // public loopback port
	// MetricsPort, when set, serves the private metrics address (Go runtime and process
	// collectors) on that loopback port.
	MetricsPort int
	// Name tells the database apart: <prefix><Name>_<random>.
	Name string
	// Env adds or overrides ARUMA_* settings.
	Env map[string]string
	// Log receives the server's output; nil discards it.
	Log io.Writer
}

// Server is an Aruma process on its own database. Start, Kill and Term may be repeated; the
// database outlives the processes until Drop.
type Server struct {
	opts      Options
	DBName    string
	dbURL     string
	adminConn *pgx.Conn
	env       []string
	URL       string // http://127.0.0.1:<port>

	mu   sync.Mutex
	cmd  *exec.Cmd
	exit chan error
}

// NewServer creates the database and migrates it. Drop removes it.
func NewServer(ctx context.Context, o Options) (*Server, error) {
	name := DBPrefix() + o.Name + "_" + RandomHex(4)
	admin, err := pgx.Connect(ctx, AdminURL())
	if err != nil {
		return nil, fmt.Errorf("connect to Postgres (ARUMA_TEST_DATABASE_URL): %w", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		return nil, err
	}
	u, _ := url.Parse(AdminURL())
	u.Path = "/" + name
	s := &Server{opts: o, DBName: name, dbURL: u.String(), adminConn: admin, URL: fmt.Sprintf("http://127.0.0.1:%d", o.Port)}
	vars := map[string]string{
		"ARUMA_DATABASE_URL":                 s.dbURL,
		"ARUMA_PUBLIC_URL":                   s.URL,
		"ARUMA_LISTEN":                       fmt.Sprintf("127.0.0.1:%d", o.Port),
		"ARUMA_METRICS_LISTEN":               "-",
		"ARUMA_MASTER_KEY":                   base64.StdEncoding.EncodeToString([]byte(RandomHex(16))),
		"ARUMA_LOG_LEVEL":                    "warn",
		"ARUMA_RTDB_ENABLED":                 "true",
		"ARUMA_RTDB_DEV_OPEN_RULES":          "true",
		"ARUMA_RTDB_CONNECTIONS_PER_IP":      "1000000",
		"ARUMA_RTDB_CONNECTIONS_PER_PROJECT": "1000000",
	}
	if o.MetricsPort > 0 {
		vars["ARUMA_METRICS_LISTEN"] = fmt.Sprintf("127.0.0.1:%d", o.MetricsPort)
	}
	for k, v := range o.Env {
		vars[k] = v
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ARUMA_") {
			s.env = append(s.env, kv)
		}
	}
	for k, v := range vars {
		s.env = append(s.env, k+"="+v)
	}
	if err := s.run(ctx, nil, "migrate", "up"); err != nil {
		s.Drop()
		return nil, err
	}
	return s, nil
}

// run runs an aruma command to completion.
func (s *Server) run(ctx context.Context, stdin io.Reader, args ...string) error {
	cmd := exec.CommandContext(ctx, s.opts.Bin, args...)
	cmd.Env, cmd.Stdin = s.env, stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aruma %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// Start starts the server and waits until it answers /healthz.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cmd != nil {
		s.mu.Unlock()
		return errors.New("server already running")
	}
	cmd := exec.Command(s.opts.Bin, "serve")
	cmd.Env = s.env
	w := s.opts.Log
	if w == nil {
		w = io.Discard
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return err
	}
	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()
	s.cmd, s.exit = cmd, exit
	s.mu.Unlock()

	hc := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exit:
			s.mu.Lock()
			s.cmd = nil
			s.mu.Unlock()
			return fmt.Errorf("aruma exited at start: %v", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if resp, err := hc.Get(s.URL + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
	return errors.New("aruma did not answer /healthz within 30 s")
}

// PID is the running server's process id, 0 when none runs.
func (s *Server) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Kill stops the server with SIGKILL: no shutdown runs, as in a crash or an OOM kill.
func (s *Server) Kill() error { return s.stop(syscall.SIGKILL, 10*time.Second) }

// Term stops the server with SIGTERM, its graceful shutdown, and waits for it to exit.
func (s *Server) Term() error { return s.stop(syscall.SIGTERM, 60*time.Second) }

func (s *Server) stop(sig syscall.Signal, wait time.Duration) error {
	s.mu.Lock()
	cmd, exit := s.cmd, s.exit
	s.cmd = nil
	s.mu.Unlock()
	if cmd == nil {
		return nil
	}
	_ = cmd.Process.Signal(sig)
	select {
	case err := <-exit:
		var ee *exec.ExitError
		if sig == syscall.SIGTERM && err != nil && !errors.As(err, &ee) {
			return err
		}
		return nil
	case <-time.After(wait):
		_ = cmd.Process.Kill()
		<-exit
		return fmt.Errorf("aruma did not exit within %s of %v", wait, sig)
	}
}

// Drop stops the server and drops its database.
func (s *Server) Drop() {
	_ = s.Kill()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = s.adminConn.Exec(ctx, "DROP DATABASE IF EXISTS "+s.DBName+" WITH (FORCE)")
	s.adminConn.Close(ctx)
}

// DB opens a connection to the server's database, for reading what the clients cannot see.
func (s *Server) DB(ctx context.Context) (*pgx.Conn, error) { return pgx.Connect(ctx, s.dbURL) }

// Seq reads a Realtime Database's write count (rtdb.databases.seq) straight from Postgres.
func (s *Server) Seq(ctx context.Context, ns string) (int64, error) {
	c, err := s.DB(ctx)
	if err != nil {
		return 0, err
	}
	defer c.Close(ctx)
	var seq int64
	err = c.QueryRow(ctx, "SELECT seq FROM rtdb.databases WHERE name = $1", ns).Scan(&seq)
	return seq, err
}

// RSSBytes is the server process's resident set size, read with ps.
func (s *Server) RSSBytes() (int64, error) {
	pid := s.PID()
	if pid == 0 {
		return 0, errors.New("server not running")
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kb << 10, err
}

// OpenFiles counts the server process's open file descriptors, read with lsof.
func (s *Server) OpenFiles() (int, error) {
	pid := s.PID()
	if pid == 0 {
		return 0, errors.New("server not running")
	}
	out, err := exec.Command("lsof", "-n", "-P", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return max(bytes.Count(out, []byte("\n"))-1, 0), nil
}

// Metrics scrapes the private metrics address and returns the plain samples (no labels) by
// name: go_goroutines, go_memstats_heap_inuse_bytes, process_resident_memory_bytes, …
func (s *Server) Metrics(ctx context.Context) (map[string]float64, error) {
	if s.opts.MetricsPort == 0 {
		return nil, errors.New("no metrics port")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/metrics", s.opts.MetricsPort), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]float64{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' || strings.ContainsRune(line, '{') {
			continue
		}
		name, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
			out[name] = f
		}
	}
	return out, sc.Err()
}

// Project creates a console account with the operator command, signs it in, and creates a
// project through the console API, as the dashboard does. It returns the namespace of the
// project's default database. The server must be running.
func (s *Server) Project(ctx context.Context, id string) (string, error) {
	email := "owner-" + RandomHex(4) + "@rtdbkit.test"
	password := RandomHex(16)
	if err := s.run(ctx, strings.NewReader(password+"\n"), "accounts", "create", "--email", email); err != nil {
		return "", err
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, _ := http.NewRequestWithContext(ctx, "POST", s.URL+"/console/v1/auth:login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login: %s: %s", resp.Status, b)
	}
	var login struct {
		CSRF string `json:"csrfToken"`
	}
	_ = json.Unmarshal(b, &login)
	var cookies []string
	for _, ck := range resp.Cookies() {
		cookies = append(cookies, ck.Name+"="+ck.Value)
	}
	body, _ = json.Marshal(map[string]string{"projectId": id, "displayName": id})
	req, _ = http.NewRequestWithContext(ctx, "POST", s.URL+"/console/v1/projects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", strings.Join(cookies, "; "))
	req.Header.Set("X-Aruma-CSRF", login.CSRF)
	resp, err = hc.Do(req)
	if err != nil {
		return "", err
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("create project: %s: %s", resp.Status, b)
	}
	return id + "-default-rtdb", nil
}

// FreePort reports whether a loopback port is free to listen on.
func FreePort(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}
