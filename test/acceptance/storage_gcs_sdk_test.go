package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// S4-B's and S4-C's done conditions, proved with the Admin SDKs themselves against a live server:
// cloud.google.com/go/storage 1.62.1 (testdata/gcs-sdk/go, a module of its own) and
// @google-cloud/storage 8.4.0 (testdata/gcs-sdk/node), configured as adminstorage and
// @aruma/admin-storage will configure them (ADR-022 §14). The SDKs go through a recording
// proxy, so the test also checks that each wire form the done conditions name was exercised:
// Node's bytes 0-*/*, its 308 with Range and its multipart, Go's multipart, its 200 with
// X-Http-Status-Code-Override: 308 and its bytes */N.
//
// Both run under a network guard, and the test fails unless the guard saw them connect to the
// recorder and nothing else: the Go program refuses every dial beyond loopback itself (its last
// step), and Node loads test/experiments/netguard.cjs.
//
// It builds a Go program with the Google libraries and installs an npm package, so it runs only
// with ARUMA_GCS_SDK=1 (Go and Node on PATH; the first run needs the module proxy and the npm
// registry, or their caches):
//
//	ARUMA_GCS_SDK=1 go test -run TestStorageGCSWithTheAdminSDKs ./test/acceptance/
func TestStorageGCSWithTheAdminSDKs(t *testing.T) {
	if os.Getenv("ARUMA_GCS_SDK") == "" {
		t.Skip("set ARUMA_GCS_SDK=1 to run the Admin SDKs' GCS scenarios (Go and Node needed)")
	}
	for _, tool := range []string{"go", "node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is not on PATH: %v", tool, err)
		}
	}
	e := newEnv(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir()})
	if e.App.Storage == nil {
		t.Fatal("Storage is not enabled")
	}
	owner := e.owner()
	bucket := owner.project("sdk-app").str("storageBucket")
	_, key := owner.serviceAccountKey("sdk-app", "backend")
	keyPath := filepath.Join(t.TempDir(), "service-account.json")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	token, _, err := exchange(t, key)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newRecorder(t, e.URL)

	gx := proxy.run(t, "go", exec.Command("go", "run", "."),
		"ARUMA_GCS_ENDPOINT="+proxy.URL+"/storage/v1/", "KEY_FILE="+keyPath, "BUCKET="+bucket, "GOWORK=off")
	gx.require(t, "Go multipart", func(x hop) bool {
		return x.Method == "POST" && x.Query.Get("uploadType") == "multipart" && x.Status == 200
	})
	gx.require(t, "Go chunk answered 200 with the 308 override", func(x hop) bool {
		return x.Method == "POST" && x.session() && x.Req.Get("X-GUploader-No-308") == "yes" && x.Status == 200 &&
			x.Resp.Get("X-Http-Status-Code-Override") == "308" && strings.HasPrefix(x.Resp.Get("Range"), "bytes=0-")
	})
	gx.require(t, "Go empty last chunk", func(x hop) bool {
		return x.Method == "POST" && x.session() && regexp.MustCompile(`^bytes \*/\d+$`).MatchString(x.Req.Get("Content-Range")) &&
			x.Req.Get("X-Goog-Hash") != "" && x.Status == 200
	})
	gx.require(t, "Go checksum refused", func(x hop) bool { return x.Status == 400 })
	gx.require(t, "Go ifGenerationMatch=0 refused", func(x hop) bool {
		return x.Query.Get("ifGenerationMatch") == "0" && x.Status == 412
	})
	gx.require(t, "Go JSON read pinned to a generation", func(x hop) bool {
		return x.Method == "GET" && x.Query.Get("alt") == "media" && x.Query.Get("generation") != "" && x.Status == 200 &&
			x.Resp.Get("X-Goog-Generation") == x.Query.Get("generation")
	})
	gx.none(t, "a real 308 to Go", func(x hop) bool { return x.Status == http.StatusPermanentRedirect })
	gx.none(t, "a Go request without Bearer", func(x hop) bool { return !strings.HasPrefix(x.Req.Get("Authorization"), "Bearer ") })

	nx := proxy.run(t, "node", exec.Command("node", "scenario.mjs"),
		"ARUMA_GCS_ORIGIN="+proxy.URL, "ACCESS_TOKEN="+token, "PROJECT_ID=sdk-app", "BUCKET="+bucket)
	nx.require(t, "Node resumable start with X-Upload-Content-Type", func(x hop) bool {
		return x.Method == "POST" && x.Query.Get("uploadType") == "resumable" && !x.session() && x.Status == 200 &&
			x.Req.Get("X-Upload-Content-Type") == "text/csv" && strings.HasPrefix(x.Resp.Get("Location"), proxy.URL+"/upload/storage/v1/")
	})
	nx.require(t, "Node save() in one request", func(x hop) bool {
		return x.Method == "PUT" && x.session() && x.Req.Get("Content-Range") == "bytes 0-*/*" && x.Status == 200
	})
	nx.require(t, "Node chunk answered 308 with Range", func(x hop) bool {
		return x.Method == "PUT" && x.session() && x.Req.Get("Content-Range") == "bytes 0-262143/*" &&
			x.Status == http.StatusPermanentRedirect && x.Resp.Get("Range") == "bytes=0-262143" && x.Resp.Get("Location") == ""
	})
	// save() knows where its buffer ends, so an object on a chunk's boundary ends with a full
	// chunk; the empty last chunk (bytes N-(N-1)/N) is a stream's, covered in gcs's own tests.
	nx.require(t, "Node last chunk on a boundary", func(x hop) bool {
		return x.Method == "PUT" && x.session() && x.Req.Get("Content-Range") == "bytes 262144-524287/524288" && x.Status == 200
	})
	nx.require(t, "Node resumable:false", func(x hop) bool {
		return x.Method == "POST" && x.Query.Get("uploadType") == "multipart" && x.Status == 200
	})
	nx.require(t, "Node checksum refused", func(x hop) bool { return x.Status == 400 })
	nx.require(t, "Node ifGenerationMatch=0 refused", func(x hop) bool {
		return x.Query.Get("ifGenerationMatch") == "0" && x.Status == 412
	})
	nx.require(t, "Node download with its checksum headers", func(x hop) bool {
		return x.Method == "GET" && x.Query.Get("alt") == "media" && x.Status == 200 &&
			x.Resp.Get("X-Goog-Stored-Content-Encoding") == "identity" && strings.HasPrefix(x.Resp.Get("X-Goog-Hash"), "crc32c=")
	})
	nx.none(t, "a Node request without Bearer", func(x hop) bool { return !strings.HasPrefix(x.Req.Get("Authorization"), "Bearer ") })
}

// hop is one request through the recorder and its reply.
type hop struct {
	Method, Path string
	Query        url.Values
	Req, Resp    http.Header
	Status       int
}

func (x hop) session() bool { return x.Query.Get("upload_id") != "" }

func (x hop) String() string {
	return fmt.Sprintf("%s %s?%s Content-Range=%q -> %d Range=%q", x.Method, x.Path, x.Query.Encode(), x.Req.Get("Content-Range"),
		x.Status, x.Resp.Get("Range"))
}

type hops []hop

func (xs hops) require(t *testing.T, what string, match func(hop) bool) {
	t.Helper()
	for _, x := range xs {
		if match(x) {
			return
		}
	}
	t.Errorf("no exchange shows %s; the exchanges:\n%s", what, xs)
}

func (xs hops) none(t *testing.T, what string, match func(hop) bool) {
	t.Helper()
	for _, x := range xs {
		if match(x) {
			t.Errorf("%s: %s", what, x)
		}
	}
}

func (xs hops) String() string {
	var b strings.Builder
	for _, x := range xs {
		b.WriteString("  " + x.String() + "\n")
	}
	return b.String()
}

// recorder is a reverse proxy in front of the server that records every exchange, and rewrites
// the session URL in Location so the chunks come back through it.
type recorder struct {
	*httptest.Server
	mu  sync.Mutex
	got hops
}

func newRecorder(t *testing.T, target string) *recorder {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) { pr.SetURL(u) },
		ModifyResponse: func(resp *http.Response) error {
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, target) {
				resp.Header.Set("Location", rec.URL+strings.TrimPrefix(loc, target))
			}
			r := resp.Request
			rec.mu.Lock()
			rec.got = append(rec.got, hop{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(),
				Req: r.Header.Clone(), Resp: resp.Header.Clone(), Status: resp.StatusCode})
			rec.mu.Unlock()
			return nil
		},
	}
	rec.Server = httptest.NewServer(rp)
	t.Cleanup(rec.Close)
	return rec
}

// run runs one SDK's scenarios in testdata/gcs-sdk/<dir> and returns the exchanges they made.
func (p *recorder) run(t *testing.T, dir string, cmd *exec.Cmd, env ...string) hops {
	t.Helper()
	cmd.Dir = filepath.Join("testdata", "gcs-sdk", dir)
	if dir == "node" {
		if _, err := os.Stat(filepath.Join(cmd.Dir, "node_modules", "@google-cloud", "storage")); err != nil {
			install := exec.Command("npm", "ci", "--no-audit", "--no-fund", "--prefer-offline")
			install.Dir = cmd.Dir
			if out, err := install.CombinedOutput(); err != nil {
				t.Fatalf("npm ci: %v\n%s", err, out)
			}
		}
	}
	p.mu.Lock()
	p.got = nil
	p.mu.Unlock()
	guardLog := filepath.Join(t.TempDir(), "netguard.log")
	if dir == "node" {
		guard, err := filepath.Abs(filepath.Join("..", "experiments", "netguard.cjs"))
		if err != nil {
			t.Fatal(err)
		}
		env = append(env, "NODE_OPTIONS=--require "+guard, "ARUMA_NETGUARD_LOG="+guardLog)
	}
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	out, err := cmd.Output()
	var res struct {
		Results []struct {
			Op     string `json:"op"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"results"`
	}
	if jerr := json.Unmarshal(lastLine(out), &res); jerr != nil || len(res.Results) == 0 {
		t.Fatalf("%s: %v %v\nstdout:\n%s\nstderr:\n%s", dir, err, jerr, out, stderr.String())
	}
	guarded := false
	for _, r := range res.Results {
		guarded = guarded || r.Op == "network guard"
		if r.OK {
			t.Logf("%s %s: ok %s", dir, r.Op, r.Detail)
		} else {
			t.Errorf("%s %s: %s", dir, r.Op, r.Detail)
		}
	}
	if err != nil && !t.Failed() {
		t.Errorf("%s: %v\n%s", dir, err, stderr.String())
	}
	switch dir {
	case "go":
		if !guarded {
			t.Error("go: no network guard step: the program ran without its guard")
		}
	case "node":
		p.netguarded(t, guardLog)
	}
	t.Logf("%s: %d steps in %v", dir, len(res.Results), time.Since(start).Round(time.Millisecond))
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got
}

// netguarded fails the test unless netguard.cjs's log shows connections to the recorder and to
// nothing else.
func (p *recorder) netguarded(t *testing.T, log string) {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Errorf("the network guard wrote nothing: %v", err)
		return
	}
	self := strings.TrimPrefix(p.URL, "http://")
	allowed := 0
	for line := range strings.Lines(string(b)) {
		verdict, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		target, _, _ := strings.Cut(rest, " ")
		if verdict != "ALLOW" || target != self {
			t.Errorf("network guard: %s", strings.TrimSpace(line))
			continue
		}
		allowed++
	}
	if allowed == 0 {
		t.Error("the network guard saw no connection to the recorder: it was not loaded")
	}
}

func lastLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	return lines[len(lines)-1]
}
