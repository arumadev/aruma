// Package storage uses adminstorage (sdk/go/adminstorage) against a live Aruma with Cloud
// Storage (ADR-022 §14): the official cloud.google.com/go/storage client, separate from
// firebase.App, on <aruma>/storage/v1/ with JSON reads. Uploads of each kind (multipart, and
// resumable with its chunks and its empty last chunk), downloads with the client's CRC check,
// DownloadURL's unified link fetched with no credential, copy, list and delete. A recorder under
// the client's authorization checks the Bearer on every request, the resumable start and chunks
// included, also with key files that carry universe_domain.
//
// Each test starts test/smoke/genconfig -serve -storage on a throwaway database that genconfig
// drops when it stops (Go and the local Postgres are needed): TestStorageLoopback on
// http://127.0.0.1:<port>, TestStorageTLS behind a TLS front at https://storage.aruma.test, the
// production form, with the key file's token_uri, the session URLs and the download URLs on that
// name. http.DefaultTransport, which adminstorage clones, sends that name's port 443 to the front,
// trusts the run's CA only, and refuses every other connection beyond loopback.
//
//	ARUMA_SMOKE_ADDR       Aruma's listen address (default 127.0.0.1:0: any free port)
//	ARUMA_SMOKE_TLS_ADDR   the TLS front's address (default 127.0.0.1:0)
//	ARUMA_SMOKE_DB_PREFIX  prefix of the throwaway database (default aruma_smoke_)
package storage

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/arumadev/aruma/sdk/go/adminstorage"
)

const host = "storage.aruma.test"

var (
	dialMu  sync.Mutex
	remotes []string
	front   string // the TLS front of the running test, for host:443
	genBin  string

	// metadataHits counts the connections to the GCE metadata server's stand-in, and
	// metadataProbed those the test made on purpose (the official SignedURL).
	metadataHits, metadataProbed atomic.Int64
)

// standInMetadata listens for cloud.google.com/go/compute/metadata, which dials through a
// transport of its own, not http.DefaultTransport: GCE_METADATA_HOST sends it here, where every
// connection is counted and closed, instead of to 169.254.169.254 (and it skips the DNS probe).
func standInMetadata() (func(), error) {
	ln, err := net.Listen("tcp", env("ARUMA_SMOKE_METADATA_ADDR", "127.0.0.1:0"))
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
	stopMetadata, err := standInMetadata()
	if err != nil {
		fmt.Fprintln(os.Stderr, "metadata stand-in:", err)
		os.Exit(1)
	}
	defer stopMetadata()
	t := http.DefaultTransport.(*http.Transport)
	dial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, port, _ := net.SplitHostPort(addr)
		dialMu.Lock()
		f := front
		dialMu.Unlock()
		if strings.EqualFold(h, host) && port == "443" && f != "" {
			return dial(ctx, network, f)
		}
		if ip := net.ParseIP(h); h != "localhost" && (ip == nil || !ip.IsLoopback()) {
			dialMu.Lock()
			remotes = append(remotes, addr)
			dialMu.Unlock()
			return nil, errors.New("network access beyond loopback blocked by the test")
		}
		return dial(ctx, network, addr)
	}
	for _, name := range []string{"STORAGE_EMULATOR_HOST", "FIREBASE_STORAGE_EMULATOR_HOST", "GOOGLE_APPLICATION_CREDENTIALS", "ARUMA_URL"} {
		_ = os.Unsetenv(name)
	}
	dir, err := os.MkdirTemp("", "aruma-smoke-storage-bin-")
	if err == nil {
		genBin = filepath.Join(dir, "genconfig")
		build := exec.Command("go", "build", "-o", genBin, "./test/smoke/genconfig")
		build.Dir, build.Stdout, build.Stderr = filepath.Join("..", "..", "..", ".."), os.Stderr, os.Stderr
		err = build.Run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "build genconfig:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	if len(remotes) > 0 {
		fmt.Fprintln(os.Stderr, "network access beyond loopback attempted:", remotes)
		code = 1
	}
	if hits, probed := metadataHits.Load(), metadataProbed.Load(); hits != probed {
		fmt.Fprintf(os.Stderr, "the GCE metadata server was asked %d times beyond the %d probes\n", hits-probed, probed)
		code = 1
	}
	stopMetadata()
	os.Exit(code)
}

// server is one running genconfig.
type server struct {
	dir      string
	origin   string // the public URL
	bucket   string
	keyFile  string
	tlsAddr  string
	expected struct {
		ServerURL     string `json:"serverUrl"`
		StorageBucket string `json:"storageBucket"`
	}
}

func start(t *testing.T, tlsFront bool) *server {
	t.Helper()
	s := &server{dir: t.TempDir()}
	args := []string{"-out", s.dir, "-serve", "-storage",
		"-addr", env("ARUMA_SMOKE_ADDR", "127.0.0.1:0"), "-db-prefix", env("ARUMA_SMOKE_DB_PREFIX", "aruma_smoke_")}
	if tlsFront {
		args = append(args, "-public-host", host, "-public-tls", env("ARUMA_SMOKE_TLS_ADDR", "127.0.0.1:0"))
	}
	cmd := exec.Command(genBin, args...)
	cmd.Dir, cmd.Stderr = filepath.Join("..", "..", "..", ".."), os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM) // genconfig drops its database on the way out
		_ = cmd.Wait()
	})
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "export ARUMA_PUBLIC_TLS_ADDR="); ok {
			s.tlsAddr = v
		}
		if strings.HasPrefix(sc.Text(), "export ARUMA_CLI_TOKEN=") {
			break
		}
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "expected.json"))
	if err == nil {
		err = json.Unmarshal(b, &s.expected)
	}
	if err != nil {
		t.Fatal(err)
	}
	s.origin, s.bucket, s.keyFile = s.expected.ServerURL, s.expected.StorageBucket, filepath.Join(s.dir, "service-account.json")

	tr := http.DefaultTransport.(*http.Transport)
	if tlsFront {
		if s.tlsAddr == "" || s.origin != "https://"+host {
			t.Fatalf("genconfig printed no TLS front, or the public URL is %q", s.origin)
		}
		ca, err := os.ReadFile(filepath.Join(s.dir, "public-ca.pem"))
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca)
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		dialMu.Lock()
		front = s.tlsAddr
		dialMu.Unlock()
		t.Cleanup(func() {
			dialMu.Lock()
			front = ""
			dialMu.Unlock()
			tr.TLSClientConfig = nil
		})
	} else if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+$`).MatchString(s.origin) {
		t.Fatalf("public URL %q", s.origin)
	}
	return s
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// recorder sits under adminstorage's authorization and records what each request carried.
type recorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	got  []exchange
}

type exchange struct {
	Method string
	URL    *url.URL
	Auth   string
	Range  string
	Status int
}

func (x exchange) String() string {
	return fmt.Sprintf("%s %s%s Content-Range=%q Authorization=%s -> %d", x.Method, x.URL.Path, "?"+x.URL.RawQuery, x.Range,
		cmp.Or(strings.SplitN(x.Auth, " ", 2)[0], "none"), x.Status)
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	x := exchange{Method: req.Method, URL: req.URL, Auth: req.Header.Get("Authorization"), Range: req.Header.Get("Content-Range")}
	if resp != nil {
		x.Status = resp.StatusCode
	}
	r.mu.Lock()
	r.got = append(r.got, x)
	r.mu.Unlock()
	return resp, err
}

func (r *recorder) since(n int) []exchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got[n:])
}

func (r *recorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func isStart(x exchange) bool {
	return x.Method == http.MethodPost && x.URL.Query().Get("uploadType") == "resumable" && !x.URL.Query().Has("upload_id")
}

func isChunk(x exchange) bool { return x.URL.Query().Has("upload_id") }

// requireBearer checks that requests went out, all to origin with a Bearer, including a
// resumable start and its chunks when resumable is set.
func requireBearer(t *testing.T, origin string, xs []exchange, resumable bool) {
	t.Helper()
	if len(xs) == 0 {
		t.Fatal("no request recorded")
	}
	if resumable && (!slices.ContainsFunc(xs, isStart) || !slices.ContainsFunc(xs, isChunk)) {
		t.Errorf("no resumable start and chunk among %v", xs)
	}
	for _, x := range xs {
		if got := x.URL.Scheme + "://" + x.URL.Host; got != origin {
			t.Errorf("a request beyond %s: %v", origin, x)
		}
		// The token exchange goes through the same transport, with the signed assertion as its
		// credential.
		if x.URL.Path != "/oauth2/v1/token" && !regexp.MustCompile(`^Bearer \S+$`).MatchString(x.Auth) {
			t.Errorf("a request without Bearer: %v", x)
		}
	}
}

func newClient(t *testing.T, s *server, keyFile string) (*adminstorage.Client, *recorder) {
	t.Helper()
	rec := &recorder{base: http.DefaultTransport.(*http.Transport).Clone()}
	c, err := adminstorage.NewClient(t.Context(), adminstorage.WithCredentialsFile(keyFile),
		adminstorage.WithDefaultBucket(s.bucket), adminstorage.WithHTTPClient(&http.Client{Transport: rec}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, rec
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

const chunk = 256 * 1024

var (
	small = []byte("hello aruma\n")
	odd   = pattern(2*chunk + 1000)
	even  = pattern(2 * chunk)
)

func write(ctx context.Context, obj *storage.ObjectHandle, data []byte, chunkSize int, contentType string) (*storage.ObjectAttrs, error) {
	w := obj.NewWriter(ctx)
	w.ChunkSize, w.ContentType = chunkSize, contentType
	w.Metadata = map[string]string{"k": "v"}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return w.Attrs(), nil
}

func read(ctx context.Context, obj *storage.ObjectHandle) ([]byte, error) {
	r, err := obj.NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r) // the reader checks the CRC32C of a full read against X-Goog-Hash
}

func TestStorageLoopback(t *testing.T) { scenario(t, start(t, false)) }

func TestStorageTLS(t *testing.T) { scenario(t, start(t, true)) }

func scenario(t *testing.T, s *server) {
	ctx := t.Context()
	c, rec := newClient(t, s, s.keyFile)
	if c.URL() != s.origin {
		t.Fatalf("URL() = %q, want %q (from the key file's token_uri)", c.URL(), s.origin)
	}
	bkt, err := c.DefaultBucket()
	if err != nil {
		t.Fatal(err)
	}
	castagnoli := crc32.MakeTable(crc32.Castagnoli)

	t.Run("signed URLs", func(t *testing.T) {
		if _, err := c.SignedURL(s.bucket, "smoke/small.txt", &storage.SignedURLOptions{Method: http.MethodGet}); !errors.Is(err, adminstorage.ErrSignedURLsUnsupported) {
			t.Errorf("SignedURL: %v", err)
		}
		// The official one, without GoogleAccessID, asks the metadata server for the signer:
		// the guard must see it, so the count at the end proves no other request did.
		before := metadataHits.Load()
		_, err := bkt.SignedURL("smoke/small.txt", &storage.SignedURLOptions{Method: http.MethodGet, Expires: time.Now().Add(time.Hour)})
		got := metadataHits.Load() - before
		metadataProbed.Add(got)
		if err == nil || got == 0 {
			t.Errorf("BucketHandle.SignedURL: %v, %d connections to the metadata stand-in; want an error after one at least", err, got)
		}
	})

	t.Run("emulator variables", func(t *testing.T) {
		for _, name := range []string{"STORAGE_EMULATOR_HOST", "FIREBASE_STORAGE_EMULATOR_HOST"} {
			t.Setenv(name, "127.0.0.1:9199")
			if _, err := adminstorage.NewClient(t.Context(), adminstorage.WithCredentialsFile(s.keyFile)); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s: NewClient returned %v", name, err)
			}
			_ = os.Unsetenv(name)
		}
	})

	t.Run("uploads", func(t *testing.T) {
		from := rec.len()
		for _, u := range []struct {
			name  string
			data  []byte
			chunk int
		}{
			{"smoke/small.txt", small, 16 << 20}, // under ChunkSize: one multipart request
			{"smoke/odd.bin", odd, chunk},        // resumable: 200 with the 308 override until the last chunk
			{"smoke/even.bin", even, chunk},      // on a chunk boundary: an empty last chunk, bytes */N
			{"smoke/single.bin", odd, 0},         // ChunkSize 0: one multipart request of any size
		} {
			attrs, err := write(ctx, bkt.Object(u.name), u.data, u.chunk, "application/octet-stream")
			if err != nil {
				t.Fatalf("%s: %v", u.name, err)
			}
			if attrs.Size != int64(len(u.data)) || attrs.CRC32C != crc32.Checksum(u.data, castagnoli) || attrs.Metadata["k"] != "v" {
				t.Errorf("%s: attrs %+v", u.name, attrs)
			}
		}
		xs := rec.since(from)
		requireBearer(t, s.origin, xs, true)
		if !slices.ContainsFunc(xs, func(x exchange) bool { return x.URL.Query().Get("uploadType") == "multipart" }) {
			t.Errorf("no multipart upload among %v", xs)
		}
		if !slices.ContainsFunc(xs, func(x exchange) bool { return isChunk(x) && regexp.MustCompile(`^bytes \*/\d+$`).MatchString(x.Range) }) {
			t.Errorf("no empty last chunk among %v", xs)
		}
	})

	t.Run("downloads", func(t *testing.T) {
		from := rec.len()
		for name, want := range map[string][]byte{"smoke/small.txt": small, "smoke/odd.bin": odd, "smoke/even.bin": even, "smoke/single.bin": odd} {
			got, err := read(ctx, bkt.Object(name))
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s: %d bytes, %v", name, len(got), err)
			}
		}
		r, err := bkt.Object("smoke/odd.bin").NewRangeReader(ctx, 10, 20)
		if err != nil {
			t.Fatal(err)
		}
		part, err := io.ReadAll(r)
		r.Close()
		if err != nil || !bytes.Equal(part, odd[10:30]) {
			t.Errorf("range: %v %v", part, err)
		}
		attrs, err := bkt.Object("smoke/small.txt").Attrs(ctx)
		if err != nil || attrs.ContentType != "application/octet-stream" || attrs.Size != int64(len(small)) {
			t.Errorf("Attrs: %+v %v", attrs, err)
		}
		requireBearer(t, s.origin, rec.since(from), false)
	})

	t.Run("DownloadURL", func(t *testing.T) {
		name := "smoke/صور/a b+c.bin"
		if _, err := write(ctx, bkt.Object(name), odd, 0, ""); err != nil {
			t.Fatal(err)
		}
		// The names as encodeURIComponent writes them, which every Firebase SDK does.
		escaped := map[string]string{"smoke/small.txt": "smoke%2Fsmall.txt", name: "smoke%2F%D8%B5%D9%88%D8%B1%2Fa%20b%2Bc.bin"}
		for name, want := range map[string][]byte{"smoke/small.txt": small, name: odd} {
			from := rec.len()
			u, err := c.DownloadURL(ctx, bkt.Object(name))
			if err != nil {
				t.Fatal(err)
			}
			re := "^" + regexp.QuoteMeta(s.origin+"/v0/b/"+s.bucket+"/o/"+escaped[name]) + `\?alt=media&token=[0-9a-f-]{36}$`
			// An explicit :443 after the host, not any port that starts with 443 (a loopback port can).
			if !regexp.MustCompile(re).MatchString(u) || regexp.MustCompile(`^[a-z]+://[^/]*:443(/|$)`).MatchString(u) {
				t.Errorf("DownloadURL(%q) = %q, want the unified form", name, u)
			}
			requireBearer(t, s.origin, rec.since(from), false)
			// Anyone with the link reads the file, with no credential.
			resp, err := http.Get(u)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !bytes.Equal(got, want) {
				t.Errorf("GET %s: %d, %d bytes", u, resp.StatusCode, len(got))
			}
			if again, err := c.DownloadURL(ctx, bkt.Object(name)); err != nil || again != u {
				t.Errorf("the second DownloadURL: %q %v", again, err)
			}
		}
		if _, err := c.DownloadURL(ctx, bkt.Object("smoke/missing")); !errors.Is(err, storage.ErrObjectNotExist) {
			t.Errorf("missing object: %v", err)
		}
	})

	t.Run("copy list delete", func(t *testing.T) {
		from := rec.len()
		dst := bkt.Object("copies/odd.bin")
		if _, err := dst.CopierFrom(bkt.Object("smoke/odd.bin")).Run(ctx); err != nil {
			t.Fatal(err)
		}
		if got, err := read(ctx, dst); err != nil || !bytes.Equal(got, odd) {
			t.Errorf("copy: %d bytes, %v", len(got), err)
		}
		var names []string
		it := bkt.Objects(ctx, &storage.Query{Prefix: "smoke/"})
		for {
			a, err := it.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, a.Name)
		}
		if want := []string{"smoke/even.bin", "smoke/odd.bin", "smoke/single.bin", "smoke/small.txt", "smoke/صور/a b+c.bin"}; !slices.Equal(names, want) {
			t.Errorf("list: %q, want %q", names, want)
		}
		if err := dst.Delete(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := dst.Attrs(ctx); !errors.Is(err, storage.ErrObjectNotExist) {
			t.Errorf("after delete: %v", err)
		}
		requireBearer(t, s.origin, rec.since(from), false)
	})

	// universe_domain in the key file changes nothing: adminstorage takes its token from
	// token_uri and never lets cloud.google.com/go/auth read the file.
	u, err := url.Parse(s.origin)
	if err != nil {
		t.Fatal(err)
	}
	for _, universe := range []string{"googleapis.com", u.Hostname()} {
		t.Run("universe_domain "+universe, func(t *testing.T) {
			path := keyWith(t, s, universe)
			c, rec := newClient(t, s, path)
			b, _ := c.DefaultBucket()
			if _, err := write(t.Context(), b.Object("universe/"+universe+"/odd.bin"), odd, chunk, ""); err != nil {
				t.Fatal(err)
			}
			if got, err := read(t.Context(), b.Object("universe/"+universe+"/odd.bin")); err != nil || !bytes.Equal(got, odd) {
				t.Errorf("read back: %d bytes, %v", len(got), err)
			}
			requireBearer(t, s.origin, rec.since(0), true)
		})
	}

	// The control: the official client reading the same key file itself. With universe_domain
	// set to the Aruma host, cloud.google.com/go/auth signs a token of its own instead of asking
	// token_uri, or refuses the universe outright; either way nothing reaches Aruma's data.
	t.Run("control: option.WithCredentialsFile with universe_domain", func(t *testing.T) {
		path := keyWith(t, s, host)
		gcs, err := storage.NewClient(t.Context(), option.WithEndpoint(s.origin+"/storage/v1/"), option.WithCredentialsFile(path), storage.WithJSONReads())
		if err != nil {
			t.Logf("NewClient refused it: %v", err)
			return
		}
		defer gcs.Close()
		_, err = gcs.Bucket(s.bucket).Object("smoke/small.txt").Attrs(t.Context())
		if err == nil {
			t.Fatal("the official client read an object with a key file carrying universe_domain: adminstorage's own exchange is not needed")
		}
		t.Logf("refused: %v", err)
	})
}

// keyWith writes the server's key file with universe_domain set.
func keyWith(t *testing.T, s *server, universe string) string {
	t.Helper()
	var key map[string]any
	b, err := os.ReadFile(s.keyFile)
	if err == nil {
		err = json.Unmarshal(b, &key)
	}
	if err != nil {
		t.Fatal(err)
	}
	key["universe_domain"] = universe
	b, _ = json.Marshal(key)
	path := filepath.Join(t.TempDir(), "service-account.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
