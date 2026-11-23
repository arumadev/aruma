// Package ops holds the operational acceptance tests: they run the real aruma binary, as an
// operator does, and check the guarantees an operator relies on: that a backup restores to the
// same service (backup_restore_test.go), that an upgrade keeps every byte (upgrade_test.go), and
// that a second process beside a running one waits as documented (rolling_test.go).
//
// The tests run with ARUMA_OPS=1. They need the PostgreSQL 18 client tools (pg_dump, pg_restore;
// ARUMA_PG_BIN or PATH) and a Postgres named by ARUMA_TEST_DATABASE_URL. They build ./cmd/aruma
// once, or use the binary in ARUMA_OPS_BIN. The upgrade tests also need ARUMA_OLD_BIN, a binary
// of an earlier release (test/ops/build-old.sh builds one from a tag); run.sh does all of it.
//
//	test/ops/run.sh [go test flags]
package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/store/storetest"
)

// lockedBuf is a log buffer a process and a test can use at once.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

// headBinary returns the aruma binary under test: ARUMA_OPS_BIN, or ./cmd/aruma built once.
// The tests run only when asked (ARUMA_OPS=1, which run.sh sets): they take minutes, and some
// wait out the 30 seconds a newcomer gives the single-instance locks.
func headBinary(t *testing.T) string {
	t.Helper()
	if os.Getenv("ARUMA_OPS") == "" {
		t.Skip("set ARUMA_OPS=1 to run the operational tests (test/ops/run.sh)")
	}
	if p := os.Getenv("ARUMA_OPS_BIN"); p != "" {
		return p
	}
	buildOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(file), "..", "..")
		dir, err := os.MkdirTemp("", "aruma-ops-bin-")
		if err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(dir, "aruma")
		cmd := exec.Command("go", "build", "-o", builtBin, "./cmd/aruma")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/aruma: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBin
}

func oldBinary(t *testing.T) string {
	t.Helper()
	p := os.Getenv("ARUMA_OLD_BIN")
	if p == "" {
		t.Skip("set ARUMA_OLD_BIN to a binary of an earlier release (test/ops/build-old.sh builds one)")
	}
	return p
}

func needPGTools(t *testing.T) {
	t.Helper()
	dir := os.Getenv("ARUMA_PG_BIN")
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if dir != "" {
			if _, err := os.Stat(filepath.Join(dir, tool)); err != nil {
				t.Skipf("%s not in ARUMA_PG_BIN", tool)
			}
		} else if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH (set ARUMA_PG_BIN)", tool)
		}
	}
}

// newMasterKey returns a fresh ARUMA_MASTER_KEY.
func newMasterKey() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// newDatabase creates an empty database on the test Postgres and drops it when the test ends.
func newDatabase(t *testing.T, tag string) string {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, storetest.AdminURL())
	if err != nil {
		t.Skipf("no Postgres (ARUMA_TEST_DATABASE_URL): %v", err)
	}
	defer admin.Close(ctx)
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	name := "aruma_ops_" + tag + "_" + hex.EncodeToString(rnd[:])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, storetest.AdminURL())
		if err != nil {
			return
		}
		defer c.Close(ctx)
		_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(storetest.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// deployment is one Aruma installation: a binary, a database, a Storage directory, a master key
// and the address it listens on.
type deployment struct {
	t      *testing.T
	bin    string
	dbURL  string
	dir    string // Storage data directory
	key    string
	port   int
	mport  int
	extra  []string // more KEY=value settings
	cmd    *exec.Cmd
	done   chan struct{}
	log    *lockedBuf
	exited error
}

func newDeployment(t *testing.T, bin, tag string) *deployment {
	t.Helper()
	return &deployment{
		t: t, bin: bin, dbURL: newDatabase(t, tag), dir: t.TempDir(), key: newMasterKey(),
		port: freePort(t), mport: freePort(t), log: &lockedBuf{},
	}
}

func (d *deployment) base() string { return fmt.Sprintf("http://127.0.0.1:%d", d.port) }

func (d *deployment) environ() []string {
	env := append(os.Environ(),
		"ARUMA_DATABASE_URL="+d.dbURL,
		"ARUMA_PUBLIC_URL="+d.base(),
		fmt.Sprintf("ARUMA_LISTEN=127.0.0.1:%d", d.port),
		fmt.Sprintf("ARUMA_METRICS_LISTEN=127.0.0.1:%d", d.mport),
		"ARUMA_MASTER_KEY="+d.key,
		"ARUMA_RTDB_ENABLED=true",
		"ARUMA_STORAGE_ENABLED=true",
		"ARUMA_STORAGE_DATA_DIR="+d.dir,
		"ARUMA_DOCUMENTS_ENABLED=true",
		"ARUMA_LOG_LEVEL=info",
		// Storage keeps 2 GiB free by default and answers 503 below it; the tests write little,
		// and must not depend on the free space of the machine they run on.
		"ARUMA_STORAGE_MIN_FREE_BYTES=67108864",
	)
	return append(env, d.extra...)
}

// run runs one aruma command to its end and returns its output.
func (d *deployment) run(stdin string, args ...string) (string, error) {
	cmd := exec.Command(d.bin, args...)
	cmd.Env = d.environ()
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (d *deployment) mustRun(stdin string, args ...string) string {
	d.t.Helper()
	out, err := d.run(stdin, args...)
	if err != nil {
		d.t.Fatalf("aruma %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// start launches `aruma serve` and waits until it is ready; it returns the start's error when
// the process exits first (a refusal to start).
func (d *deployment) start() error {
	d.t.Helper()
	d.log = &lockedBuf{}
	if err := d.launch(); err != nil {
		return err
	}
	deadline := time.After(90 * time.Second)
	for {
		select {
		case <-d.done:
			d.cmd = nil
			return fmt.Errorf("aruma serve exited: %v\n%s", d.exited, d.log.String())
		case <-deadline:
			d.stop()
			return fmt.Errorf("aruma serve not ready in 90s\n%s", d.log.String())
		case <-time.After(100 * time.Millisecond):
		}
		resp, err := http.Get(d.base() + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
	}
}

func (d *deployment) mustStart() {
	d.t.Helper()
	if err := d.start(); err != nil {
		d.t.Fatal(err)
	}
}

// launch starts `aruma serve` in the background, its output into d.log.
func (d *deployment) launch() error {
	cmd := exec.Command(d.bin, "serve")
	cmd.Env = d.environ()
	cmd.Stdout, cmd.Stderr = d.log, d.log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	d.cmd = cmd
	d.done = make(chan struct{})
	go func() {
		d.exited = cmd.Wait()
		close(d.done)
	}()
	d.t.Cleanup(func() { d.stop() })
	return nil
}

// stop ends the server with SIGTERM, as a service manager does, and waits for it.
func (d *deployment) stop() {
	if d.cmd == nil {
		return
	}
	_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-d.done:
	case <-time.After(30 * time.Second):
		_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
		<-d.done
	}
	d.cmd = nil
}

// kill ends the server at once, as a crash or a power cut does.
func (d *deployment) kill() {
	if d.cmd == nil {
		return
	}
	_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
	<-d.done
	d.cmd = nil
}

// ---- HTTP -----------------------------------------------------------------------------------

type reply struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r reply) obj() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Body, &m)
	return m
}

// full is the status and the whole body.
func (r reply) full() string { return fmt.Sprintf("%d %s", r.Status, r.Body) }

func (r reply) String() string {
	b := string(r.Body)
	if len(b) > 600 {
		b = b[:600] + "..."
	}
	return fmt.Sprintf("%d %s", r.Status, b)
}

// str walks nested objects and returns the string at the end of path.
func (r reply) str(path ...string) string {
	var v any = r.obj()
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[p]
	}
	s, _ := v.(string)
	return s
}

type api struct {
	t    *testing.T
	base string
	hc   *http.Client
}

func newAPI(t *testing.T, base string) *api {
	return &api{t: t, base: base, hc: &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil}}}
}

// do sends a request; body may be nil, []byte, string, io.Reader or a value to encode as JSON.
func (a *api) do(method, path string, hdr map[string]string, body any) reply {
	a.t.Helper()
	r, err := a.try(method, path, hdr, body)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	return r
}

func (a *api) try(method, path string, hdr map[string]string, body any) (reply, error) {
	var rd io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	case io.Reader:
		rd = b
	default:
		enc, err := json.Marshal(b)
		if err != nil {
			return reply{}, err
		}
		rd, ctype = bytes.NewReader(enc), "application/json"
	}
	u := path
	if !strings.HasPrefix(path, "http") {
		u = a.base + path
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return reply{}, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: b}, err
}

// must sends a request and fails the test unless the status is 2xx.
func (a *api) must(method, path string, hdr map[string]string, body any) reply {
	a.t.Helper()
	r := a.do(method, path, hdr, body)
	if r.Status < 200 || r.Status > 299 {
		a.t.Fatalf("%s %s: %s", method, path, r)
	}
	return r
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

// getenvOld is ARUMA_OLD_BIN, or "".
func getenvOld() string { return os.Getenv("ARUMA_OLD_BIN") }
