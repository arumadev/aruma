package acceptance

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// S5-D's done condition, proved with firebase-tools 15.34.0 itself (testdata/firebase-cli)
// against a live server: firebase init storage writes the template, then the published rules;
// firebase deploy --only storage publishes and the rules decide /v0 at once, both ways; a second
// deploy without a change is "already up to date"; a compile error is printed as [E] line:column;
// and without Storage the CLI says "Firebase Storage has not been set up".
//
// The CLI is configured as docs/research/experiments.md (T23) and C §5.1 say a developer
// configures it, through the environment only, and reaches the server through a TLS front that
// records every request (the CLI sends its credential only to https origins). It runs with a
// home of its own, test/experiments/netguard.cjs refusing every connection that leaves loopback,
// and on macOS under sandbox-exec with outbound network limited to loopback; the test fails on
// any refused attempt.
//
// It installs firebase-tools from the npm registry (or its cache) on the first run, so it runs
// only with ARUMA_FIREBASE_CLI=1 (Node and npm on PATH):
//
//	ARUMA_FIREBASE_CLI=1 go test -run TestStorageWithTheFirebaseCLI ./test/acceptance/
func TestStorageWithTheFirebaseCLI(t *testing.T) {
	if os.Getenv("ARUMA_FIREBASE_CLI") == "" {
		t.Skip("set ARUMA_FIREBASE_CLI=1 to run firebase-tools against the server (Node and npm needed)")
	}
	bin := firebaseToolsBin(t)
	template, err := os.ReadFile(filepath.Join(filepath.Dir(bin), "..", "firebase-tools", "templates", "init", "storage", "storage.rules"))
	if err != nil {
		t.Fatal(err)
	}

	e := storageMgmtEnv(t)
	owner := e.owner()
	bucket := owner.project("cli-app").str("storageBucket")
	cli := newFirebaseCLI(t, bin, e.URL, owner.pat())
	b, err := e.App.Storage.Bucket(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.App.Storage.Insert(t.Context(), b, storage.Principal{Kind: storage.PrincipalAdmin},
		storage.UploadSpec{Name: "public/hello.txt", Declared: 5, Surface: storage.SurfaceGCS}, strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	// An anonymous getMetadata on /v0, as the client SDKs send it: the rules alone decide it.
	read := func() int {
		t.Helper()
		resp, err := http.Get(e.URL + "/v0/b/" + bucket + "/o/public%2Fhello.txt")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := read(); s != 403 {
		t.Fatalf("before any deploy /v0 answers %d, want 403 (no release)", s)
	}

	// init with nothing published: the template, byte for byte, and the single form in
	// firebase.json (C §2.1, §2.2).
	proj := cli.dir("project")
	out := cli.ok(proj, "init", "storage", "--project", "cli-app", "--non-interactive")
	if got := cli.file(proj, "storage.rules"); got != string(template) {
		t.Errorf("init without a release wrote\n%s\nwant the template\n%s", got, template)
	}
	var config struct {
		Storage struct {
			Rules string `json:"rules"`
		} `json:"storage"`
	}
	if err := json.Unmarshal([]byte(cli.file(proj, "firebase.json")), &config); err != nil || config.Storage.Rules != "storage.rules" {
		t.Errorf("firebase.json: %v %s", err, cli.file(proj, "firebase.json"))
	}
	if strings.Contains(out, "Downloaded the existing") {
		t.Errorf("init without a release says it downloaded rules:\n%s", out)
	}
	cli.sent("init", []string{
		"GET /v1/projects/cli-app/services/127.0.0.1 200",
		"GET /v1alpha/projects/cli-app/defaultBucket 200",
		"GET /v1/projects/cli-app/releases?pageSize=10&pageToken= 200",
	})

	// The first deploy: :test, the releases, a new ruleset, PATCH 404 then POST (C §1.5).
	cli.write(proj, "storage.rules", cliOpenRules)
	out = cli.ok(proj, "deploy", "--only", "storage", "--project", "cli-app", "--non-interactive")
	if s := read(); s != 200 {
		t.Errorf("after deploying rules that open public/, /v0 answers %d, want 200", s)
	}
	for _, want := range []string{"rules file storage.rules compiled successfully", "released rules storage.rules to firebase.storage", "Deploy complete!"} {
		if !strings.Contains(out, want) {
			t.Errorf("deploy does not print %q:\n%s", want, out)
		}
	}
	release := "/v1/projects/cli-app/releases/firebase.storage/" + bucket
	cli.sent("the first deploy", []string{
		"GET /v1alpha/projects/cli-app/defaultBucket 200",
		"POST /v1/projects/cli-app:test 200",
		"GET /v1/projects/cli-app/releases?pageSize=10&pageToken= 200",
		"POST /v1/projects/cli-app/rulesets 200",
		"PATCH " + release + " 404",
		"POST /v1/projects/cli-app/releases 200",
	})

	// The same rules again: the CLI finds them released and uploads nothing.
	out = cli.ok(proj, "deploy", "--only", "storage", "--project", "cli-app", "--non-interactive")
	if !strings.Contains(out, "latest version of storage.rules already up to date, skipping upload...") {
		t.Errorf("the second deploy does not say already up to date:\n%s", out)
	}
	cli.sent("the second deploy", []string{
		"GET /v1alpha/projects/cli-app/defaultBucket 200",
		"POST /v1/projects/cli-app:test 200",
		"GET /v1/projects/cli-app/releases?pageSize=10&pageToken= 200",
		"GET /v1/projects/cli-app/rulesets/{id} 200",
		"PATCH " + release + " 200",
	})
	if s := read(); s != 200 {
		t.Errorf("after the second deploy /v0 answers %d, want 200", s)
	}

	// Changed rules: the next /v0 request is decided by them.
	cli.write(proj, "storage.rules", cliClosedRules)
	cli.ok(proj, "deploy", "--only", "storage", "--project", "cli-app", "--non-interactive")
	if s := read(); s != 403 {
		t.Errorf("after deploying rules that close public/, /v0 answers %d, want 403", s)
	}
	cli.sent("a deploy of changed rules", []string{
		"POST /v1/projects/cli-app:test 200",
		"GET /v1/projects/cli-app/rulesets/{id} 200",
		"POST /v1/projects/cli-app/rulesets 200",
		"PATCH " + release + " 200",
	})

	// init in another directory: the published rules, as deployed.
	other := cli.dir("other")
	out = cli.ok(other, "init", "storage", "--project", "cli-app", "--non-interactive")
	if got := cli.file(other, "storage.rules"); got != cliClosedRules {
		t.Errorf("init after a deploy wrote\n%s\nwant the published rules\n%s", got, cliClosedRules)
	}
	if !strings.Contains(out, "Downloaded the existing Storage Security Rules from the Firebase console") {
		t.Errorf("init after a deploy does not say it downloaded the rules:\n%s", out)
	}
	cli.sent("init after a deploy", []string{
		"GET /v1alpha/projects/cli-app/defaultBucket 200",
		"GET /v1/projects/cli-app/releases?pageSize=10&pageToken= 200",
		"GET /v1/projects/cli-app/rulesets/{id} 200",
	})

	// A compile error: printed where it is, and nothing is uploaded or released.
	cli.write(proj, "storage.rules", cliBrokenRules)
	out, err = cli.run(proj, "deploy", "--only", "storage", "--project", "cli-app", "--non-interactive")
	if err == nil {
		t.Errorf("a deploy of rules that do not compile succeeds:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^\[E\] 4:\d+ - \S`).MatchString(out) || !strings.Contains(out, "Compilation error in storage.rules") {
		t.Errorf("the compile error is not printed as [E] line:column:\n%s", out)
	}
	cli.sent("a deploy that does not compile", []string{"POST /v1/projects/cli-app:test 200"})
	cli.notSent("a deploy that does not compile", "/rulesets", "/releases")
	if s := read(); s != 403 {
		t.Errorf("after a refused deploy /v0 answers %d, want 403 (the rules before it)", s)
	}

	// Without Storage: init and deploy stop at defaultBucket with the CLI's own message.
	off := newEnv(t, nil)
	offOwner := off.owner()
	offOwner.project("cli-app")
	offCLI := newFirebaseCLI(t, bin, off.URL, offOwner.pat())
	notSetUp := "Firebase Storage has not been set up on project 'cli-app'"
	offProj := offCLI.dir("project")
	if out, err := offCLI.run(offProj, "init", "storage", "--project", "cli-app", "--non-interactive"); err == nil || !strings.Contains(out, notSetUp) {
		t.Errorf("init without Storage: %v\n%s", err, out)
	}
	offCLI.write(offProj, "firebase.json", `{"storage": {"rules": "storage.rules"}}`)
	offCLI.write(offProj, "storage.rules", cliOpenRules)
	if out, err := offCLI.run(offProj, "deploy", "--only", "storage", "--project", "cli-app", "--non-interactive"); err == nil || !strings.Contains(out, notSetUp) {
		t.Errorf("deploy without Storage: %v\n%s", err, out)
	}
	offCLI.sent("deploy without Storage", []string{"GET /v1alpha/projects/cli-app/defaultBucket 404"})
	offCLI.notSent("deploy without Storage", "/rulesets", "/releases", ":test")
}

const cliOpenRules = `rules_version = '2';
// Deployed by TestStorageWithTheFirebaseCLI.
service firebase.storage {
  match /b/{bucket}/o {
    match /public/{all=**} {
      allow read: if true;
    }
  }
}
`

const cliClosedRules = `rules_version = '2';
// Deployed by TestStorageWithTheFirebaseCLI: public/ for signed-in users only.
service firebase.storage {
  match /b/{bucket}/o {
    match /public/{all=**} {
      allow read: if request.auth != null;
    }
  }
}
`

const cliBrokenRules = `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    allow read: if ;
  }
}
`

// firebaseToolsBin installs testdata/firebase-cli once and returns its firebase.
func firebaseToolsBin(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is not on PATH: %v", tool, err)
		}
	}
	dir, err := filepath.Abs(filepath.Join("testdata", "firebase-cli"))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "node_modules", ".bin", "firebase")
	if _, err := os.Stat(bin); err != nil {
		install := exec.Command("npm", "ci", "--no-audit", "--no-fund", "--prefer-offline")
		install.Dir = dir
		if out, err := install.CombinedOutput(); err != nil {
			t.Fatalf("npm ci: %v\n%s", err, out)
		}
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "15.34.0" {
		t.Fatalf("firebase --version: %q %v", out, err)
	}
	return bin
}

// firebaseCLI runs firebase-tools against one server through a recording TLS front.
type firebaseCLI struct {
	t        *testing.T
	bin      string
	home     string
	env      []string
	guardLog string
	front    *httptest.Server

	mu   sync.Mutex
	hops []string // "METHOD path?query status" of the current command
}

func newFirebaseCLI(t *testing.T, bin, target, token string) *firebaseCLI {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	c := &firebaseCLI{t: t, bin: bin, home: t.TempDir()}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) { pr.SetURL(u) },
		ModifyResponse: func(resp *http.Response) error {
			r := resp.Request
			hop := r.Method + " " + r.URL.EscapedPath()
			if r.URL.RawQuery != "" {
				hop += "?" + r.URL.RawQuery
			}
			c.mu.Lock()
			c.hops = append(c.hops, fmt.Sprintf("%s %d", hop, resp.StatusCode))
			c.mu.Unlock()
			return nil
		},
	}
	c.front = httptest.NewTLSServer(rp)
	t.Cleanup(c.front.Close)

	ca := filepath.Join(c.home, "front-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.front.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	guard, err := filepath.Abs(filepath.Join("..", "experiments", "netguard.cjs"))
	if err != nil {
		t.Fatal(err)
	}
	c.guardLog = filepath.Join(c.home, "netguard.log")
	c.env = []string{
		// What a developer sets (docs/research/experiments.md T23, C §5.1): the management API
		// origins, the CLI token, the informational IAM check off, and no update notifier.
		"FIREBASE_API_URL=" + c.front.URL,
		"FIREBASE_TOKEN_URL=" + c.front.URL,
		"FIREBASE_RULES_URL=" + c.front.URL,
		"FIREBASE_FIREBASESTORAGE_URL=" + c.front.URL,
		"FIREBASE_SERVICE_USAGE_URL=" + c.front.URL,
		"FIREBASE_TOKEN=" + token,
		"FIREBASE_SKIP_INFORMATIONAL_IAM=1",
		"CI=true",
		"NO_UPDATE_NOTIFIER=1",
		// Scaffolding: a home of its own (no login or configstore of this machine), the
		// front's certificate, and the guard.
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + c.home,
		"XDG_CONFIG_HOME=" + filepath.Join(c.home, ".config"),
		"TMPDIR=" + c.home,
		"NODE_EXTRA_CA_CERTS=" + ca,
		"NODE_OPTIONS=--require " + guard,
		"ARUMA_NETGUARD_LOG=" + c.guardLog,
	}
	// At the end of the test, also when a command fails it.
	t.Cleanup(c.guarded)
	return c
}

// dir makes a directory under the CLI's home.
func (c *firebaseCLI) dir(name string) string {
	c.t.Helper()
	d := filepath.Join(c.home, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		c.t.Fatal(err)
	}
	return d
}

func (c *firebaseCLI) write(dir, name, content string) {
	c.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

func (c *firebaseCLI) file(dir, name string) string {
	c.t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		c.t.Error(err)
	}
	return string(b)
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// run runs firebase with args in dir and returns what it printed, stdout and stderr together;
// the requests it sent are kept for sent and notSent.
func (c *firebaseCLI) run(dir string, args ...string) (string, error) {
	c.t.Helper()
	c.mu.Lock()
	c.hops = nil
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.t.Context(), 3*time.Minute)
	defer cancel()
	argv := append([]string{c.bin}, args...)
	if runtime.GOOS == "darwin" {
		if sb, err := exec.LookPath("sandbox-exec"); err == nil {
			argv = append([]string{sb, "-p", loopbackOnly}, argv...)
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = c.env
	start := time.Now()
	b, err := cmd.CombinedOutput()
	out := ansiEscape.ReplaceAllString(string(b), "")
	c.t.Logf("$ firebase %s (%v, %v)\n%s  sent:\n  %s", strings.Join(args, " "), err, time.Since(start).Round(time.Millisecond),
		out, strings.Join(c.sentHops(), "\n  "))
	return out, err
}

// loopbackOnly is the sandbox-exec profile of test/experiments/firebase-cli.sh: outbound network
// to loopback and Unix sockets only.
const loopbackOnly = `(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip "localhost:*"))(allow network-outbound (remote unix-socket))`

// ok is run, failing the test unless the CLI succeeds.
func (c *firebaseCLI) ok(dir string, args ...string) string {
	c.t.Helper()
	out, err := c.run(dir, args...)
	if err != nil {
		c.t.Fatalf("firebase %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

var rulesetID = regexp.MustCompile(`/rulesets/[0-9a-f-]{36}\b`)

// sent checks that the last command sent each of want, in that order among its requests; a
// ruleset's id is written {id}.
func (c *firebaseCLI) sent(what string, want []string) {
	c.t.Helper()
	got := c.sentHops()
	i := 0
	for _, h := range got {
		if i < len(want) && rulesetID.ReplaceAllString(h, "/rulesets/{id}") == want[i] {
			i++
		}
	}
	if i < len(want) {
		c.t.Errorf("%s: %q not sent in order; the requests:\n  %s", what, want[i], strings.Join(got, "\n  "))
	}
}

// notSent checks that no request of the last command has a path containing any of parts.
func (c *firebaseCLI) notSent(what string, parts ...string) {
	c.t.Helper()
	for _, h := range c.sentHops() {
		path := strings.Fields(h)[1]
		if slices.ContainsFunc(parts, func(p string) bool { return strings.Contains(path, p) }) {
			c.t.Errorf("%s sent %s", what, h)
		}
	}
}

func (c *firebaseCLI) sentHops() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.hops)
}

// guarded checks the guard's record of every command: no connection refused, none but to the
// front, and no name resolved.
func (c *firebaseCLI) guarded() {
	c.t.Helper()
	b, err := os.ReadFile(c.guardLog)
	if err != nil {
		c.t.Errorf("the network guard wrote nothing: %v", err)
		return
	}
	front := strings.TrimPrefix(c.front.URL, "https://")
	allowed := 0
	for line := range strings.Lines(string(b)) {
		verdict, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		target, _, _ := strings.Cut(rest, " ")
		if verdict != "ALLOW" || target != front {
			c.t.Errorf("network guard: %s", strings.TrimSpace(line))
			continue
		}
		allowed++
	}
	if allowed == 0 {
		c.t.Error("the network guard saw no connection to the front: it was not loaded")
	}
}
