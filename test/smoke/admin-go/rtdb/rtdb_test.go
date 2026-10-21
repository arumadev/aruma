// Package rtdb uses firebase-admin-go's Realtime Database client against a live Aruma in its
// production form: databaseURL https://smoke-app-default-rtdb.rtdb.aruma.test behind TLS
// (ADR-018). The Go SDK speaks REST only (<path>.json with ETags, queries and
// auth_variable_override), so this covers Aruma's REST API as an official client uses it.
//
// TestMain builds test/smoke/genconfig and starts it with -serve -rtdb-domain rtdb.aruma.test and
// its TLS front, on a throwaway database that genconfig drops when it stops (Go and the local
// Postgres are needed). ARUMA_SMOKE_DIR with ARUMA_RTDB_TLS_ADDR uses a server already running in
// that form instead. http.DefaultTransport, which the Google transports clone, then sends the
// database host's port 443 to the TLS front, trusts the run's CA only, and refuses every other
// connection beyond loopback.
//
//	ARUMA_SMOKE_ADDR       Aruma's listen address (default 127.0.0.1:0: any free port)
//	ARUMA_SMOKE_TLS_ADDR   the TLS front's address (default 127.0.0.1:0)
//	ARUMA_SMOKE_DB_PREFIX  prefix of the throwaway database (default aruma_smoke_)
package rtdb

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/db"
	"firebase.google.com/go/v4/errorutils"
	"google.golang.org/api/option"
	htransport "google.golang.org/api/transport/http"
)

const domain = "rtdb.aruma.test"

var live struct {
	dir, tlsAddr string
	expected     struct {
		ProjectID   string `json:"projectId"`
		DatabaseURL string `json:"databaseURL"`
	}
}

var (
	dialMu  sync.Mutex
	remotes []string
)

func TestMain(m *testing.M) {
	stop, err := startAruma()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start Aruma:", err)
		os.Exit(1)
	}
	code := 1
	if err := routeToFront(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	} else {
		code = m.Run()
	}
	stop()
	if len(remotes) > 0 {
		fmt.Fprintln(os.Stderr, "network access beyond loopback attempted:", remotes)
		code = 1
	}
	os.Exit(code)
}

// routeToFront makes http.DefaultTransport reach <name>.rtdb.aruma.test:443 at the TLS front,
// as DNS and Caddy would, and nothing else beyond loopback.
func routeToFront() error {
	ca, err := os.ReadFile(filepath.Join(live.dir, "rtdb-ca.pem"))
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return errors.New("rtdb-ca.pem holds no certificate")
	}
	t := http.DefaultTransport.(*http.Transport)
	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	dial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, _ := net.SplitHostPort(addr)
		if strings.HasSuffix(strings.ToLower(host), "."+domain) && port == "443" {
			return dial(ctx, network, live.tlsAddr)
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			dialMu.Lock()
			remotes = append(remotes, addr)
			dialMu.Unlock()
			return nil, errors.New("network access beyond loopback blocked by the test")
		}
		return dial(ctx, network, addr)
	}
	return nil
}

func startAruma() (func(), error) {
	live.dir, live.tlsAddr = os.Getenv("ARUMA_SMOKE_DIR"), os.Getenv("ARUMA_RTDB_TLS_ADDR")
	stop := func() {}
	if live.dir == "" || live.tlsAddr == "" {
		root, err := filepath.Abs("../../../..")
		if err != nil {
			return nil, err
		}
		if live.dir, err = os.MkdirTemp("", "aruma-smoke-rtdb-"); err != nil {
			return nil, err
		}
		bin := filepath.Join(live.dir, "genconfig")
		build := exec.Command("go", "build", "-o", bin, "./test/smoke/genconfig")
		build.Dir, build.Stdout, build.Stderr = root, os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			_ = os.RemoveAll(live.dir)
			return nil, err
		}
		cmd := exec.Command(bin, "-out", live.dir, "-serve",
			"-addr", env("ARUMA_SMOKE_ADDR", "127.0.0.1:0"),
			"-db-prefix", env("ARUMA_SMOKE_DB_PREFIX", "aruma_smoke_"),
			"-rtdb-domain", domain, "-rtdb-tls", env("ARUMA_SMOKE_TLS_ADDR", "127.0.0.1:0"))
		cmd.Dir, cmd.Stderr = root, os.Stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		stop = func() {
			_ = cmd.Process.Signal(syscall.SIGTERM) // genconfig drops its database on the way out
			_ = cmd.Wait()
			_ = os.RemoveAll(live.dir)
		}
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "export ARUMA_RTDB_TLS_ADDR="); ok {
				live.tlsAddr = v
			}
			if strings.HasPrefix(sc.Text(), "export ARUMA_CLI_TOKEN=") {
				break
			}
		}
		if live.tlsAddr == "" {
			stop()
			return nil, errors.New("genconfig printed no TLS front address")
		}
	}
	b, err := os.ReadFile(filepath.Join(live.dir, "expected.json"))
	if err == nil {
		err = json.Unmarshal(b, &live.expected)
	}
	if err == nil && live.expected.DatabaseURL != "https://"+live.expected.ProjectID+"-default-rtdb."+domain {
		err = fmt.Errorf("databaseURL %q is not the production form", live.expected.DatabaseURL)
	}
	if err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func keyFile() string { return filepath.Join(live.dir, "service-account.json") }

// client returns the database client of an app made from the key file, with the auth
// override given (nil: none, the full admin access).
func client(t *testing.T, override *map[string]interface{}) *db.Client {
	t.Helper()
	ctx := context.Background()
	app, err := firebase.NewApp(ctx, &firebase.Config{DatabaseURL: live.expected.DatabaseURL, AuthOverride: override}, option.WithCredentialsFile(keyFile()))
	if err != nil {
		t.Fatal(err)
	}
	c, err := app.Database(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// rules is what the tests run under: users/$uid for their owner only, the indexes the queries
// need, and one public node.
var rules = map[string]any{"rules": map[string]any{
	"users":  map[string]any{"$uid": map[string]any{".read": "auth != null && auth.uid == $uid", ".write": "auth != null && auth.uid == $uid"}},
	"scores": map[string]any{".indexOn": []any{"score"}},
	"values": map[string]any{".indexOn": ".value"},
	"public": map[string]any{".read": true},
}}

var rulesOnce sync.Once

// setRules loads rules with PUT /.settings/rules.json, as firebase-tools and the Node Admin SDK
// do, with an access token for the key file (the Go SDK has no rules API).
func setRules(t *testing.T) {
	t.Helper()
	var err error
	rulesOnce.Do(func() {
		var b []byte
		b, err = rulesRequest(http.MethodPut, rules)
		if err == nil {
			var got any
			if b, err = rulesRequest(http.MethodGet, nil); err == nil {
				err = json.Unmarshal(b, &got)
			}
			if err == nil && !reflect.DeepEqual(got, rules) {
				err = fmt.Errorf("rules read back: %s", b)
			}
		}
	})
	if err != nil {
		t.Fatal("rules:", err)
	}
}

func rulesRequest(method string, body any) ([]byte, error) {
	ctx := context.Background()
	hc, _, err := htransport.NewClient(ctx, option.WithCredentialsFile(keyFile()),
		option.WithScopes("https://www.googleapis.com/auth/firebase.database", "https://www.googleapis.com/auth/userinfo.email"))
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, live.expected.DatabaseURL+"/.settings/rules.json", rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("%s: %d %s", method, resp.StatusCode, b)
	}
	return b, err
}

func get(t *testing.T, r *db.Ref) any {
	t.Helper()
	var v any
	if err := r.Get(context.Background(), &v); err != nil {
		t.Fatalf("Get %s: %v", r.Path, err)
	}
	return v
}

func equal(t *testing.T, what string, got, want any) {
	t.Helper()
	// Through JSON, so that numbers compare as the SDK decodes them (float64).
	var w any
	b, _ := json.Marshal(want)
	_ = json.Unmarshal(b, &w)
	if !reflect.DeepEqual(got, w) {
		gb, _ := json.Marshal(got)
		t.Fatalf("%s: got %s, want %s", what, gb, b)
	}
}

var permissionDenied = regexp.MustCompile(`(?i)permission.denied`)

// denied checks that err is the REST answer to a request the rules refuse: 401 Permission
// denied.
func denied(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: allowed", what)
	}
	if resp := errorutils.HTTPResponse(err); resp == nil || resp.StatusCode != http.StatusUnauthorized || !permissionDenied.MatchString(err.Error()) {
		t.Fatalf("%s: want 401 Permission denied, got %v", what, err)
	}
}

func TestSetGetUpdateDelete(t *testing.T) {
	ctx := context.Background()
	c := client(t, nil)
	root := c.NewRef("go/basic")
	if err := root.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	a := root.Child("a")
	if err := a.Set(ctx, map[string]any{"n": 1, "s": "سلام", "nested": map[string]any{"t": true}}); err != nil {
		t.Fatal(err)
	}
	equal(t, "after Set", get(t, a), map[string]any{"n": 1, "s": "سلام", "nested": map[string]any{"t": true}})
	equal(t, "a child", get(t, a.Child("nested/t")), true)
	equal(t, "a missing node", get(t, root.Child("missing")), nil)

	var typed struct {
		N int    `json:"n"`
		S string `json:"s"`
	}
	if err := a.Get(ctx, &typed); err != nil || typed.N != 1 || typed.S != "سلام" {
		t.Fatalf("Get into a struct: %+v %v", typed, err)
	}

	if err := root.Update(ctx, map[string]any{"a/n": 2, "a/nested": nil, "b/y": "new"}); err != nil {
		t.Fatal(err)
	}
	equal(t, "after Update", get(t, root), map[string]any{"a": map[string]any{"n": 2, "s": "سلام"}, "b": map[string]any{"y": "new"}})

	if err := root.Child("b").Delete(ctx); err != nil {
		t.Fatal(err)
	}
	equal(t, "after Delete", get(t, root), map[string]any{"a": map[string]any{"n": 2, "s": "سلام"}})
	if err := root.Child("b").Delete(ctx); err != nil {
		t.Fatalf("deleting a missing node: %v", err)
	}
}

func TestPush(t *testing.T) {
	ctx := context.Background()
	list := client(t, nil).NewRef("go/list")
	if err := list.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := list.Push(ctx, map[string]any{"i": 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := list.Push(ctx, nil) // the SDK writes "" for nil
	if err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile(`^[-0-9A-Za-z_]{20}$`)
	if !key.MatchString(first.Key) || !key.MatchString(second.Key) || first.Key >= second.Key {
		t.Fatalf("push keys %q, %q", first.Key, second.Key)
	}
	equal(t, "the list", get(t, list), map[string]any{first.Key: map[string]any{"i": 1}, second.Key: ""})
}

func TestTransaction(t *testing.T) {
	ctx := context.Background()
	clients := []*db.Client{client(t, nil), client(t, nil)}
	counter := clients[0].NewRef("go/counter")
	if err := counter.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	// Ten increments from two clients at once: each write carries the ETag it read (If-Match),
	// and a stale one must come back 412 so the SDK runs the update again.
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := range 10 {
		wg.Go(func() {
			errs <- clients[i%2].NewRef("go/counter").Transaction(ctx, func(n db.TransactionNode) (any, error) {
				var v int
				if err := n.Unmarshal(&v); err != nil {
					return nil, err
				}
				return v + 1, nil
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	equal(t, "the counter", get(t, counter), 10)

	// An error from the update function aborts without writing.
	stop := errors.New("abort")
	if err := counter.Transaction(ctx, func(db.TransactionNode) (any, error) { return nil, stop }); err != stop {
		t.Fatalf("abort: %v", err)
	}
	equal(t, "after the abort", get(t, counter), 10)
}

func TestETags(t *testing.T) {
	ctx := context.Background()
	ref := client(t, nil).NewRef("go/etag")
	if err := ref.Set(ctx, map[string]any{"v": 1}); err != nil {
		t.Fatal(err)
	}
	var v any
	etag, err := ref.GetWithETag(ctx, &v)
	if err != nil || etag == "" {
		t.Fatalf("GetWithETag: %q %v", etag, err)
	}
	equal(t, "GetWithETag", v, map[string]any{"v": 1})

	changed, same, err := ref.GetIfChanged(ctx, etag, &v)
	if err != nil || changed || same != etag {
		t.Fatalf("GetIfChanged with the current ETag: %v %q %v", changed, same, err)
	}

	ok, err := ref.SetIfUnchanged(ctx, etag, map[string]any{"v": 2})
	if err != nil || !ok {
		t.Fatalf("SetIfUnchanged with the current ETag: %v %v", ok, err)
	}
	ok, err = ref.SetIfUnchanged(ctx, etag, map[string]any{"v": 3})
	if err != nil || ok {
		t.Fatalf("SetIfUnchanged with a stale ETag: %v %v", ok, err)
	}
	equal(t, "after the refused write", get(t, ref), map[string]any{"v": 2})

	var now any
	changed, fresh, err := ref.GetIfChanged(ctx, etag, &now)
	if err != nil || !changed || fresh == "" || fresh == etag {
		t.Fatalf("GetIfChanged with a stale ETag: %v %q %v", changed, fresh, err)
	}
	equal(t, "GetIfChanged", now, map[string]any{"v": 2})
	if again, err := ref.GetWithETag(ctx, &v); err != nil || again != fresh {
		t.Fatalf("the ETag of an unchanged node changed: %q, then %q (%v)", fresh, again, err)
	}

	// A missing node has an ETag as well: writing only if it is still missing.
	missing := ref.Child("missing")
	etag, err = missing.GetWithETag(ctx, &v)
	if err != nil || etag == "" || v != nil {
		t.Fatalf("GetWithETag of a missing node: %q %v %v", etag, v, err)
	}
	if ok, err := missing.SetIfUnchanged(ctx, etag, "created"); err != nil || !ok {
		t.Fatalf("SetIfUnchanged on a missing node: %v %v", ok, err)
	}
	equal(t, "created", get(t, missing), "created")
}

func TestGetShallow(t *testing.T) {
	ctx := context.Background()
	ref := client(t, nil).NewRef("go/shallow")
	if err := ref.Set(ctx, map[string]any{"a": map[string]any{"x": 1}, "b": 2, "c": "s", "d": true}); err != nil {
		t.Fatal(err)
	}
	var v any
	if err := ref.GetShallow(ctx, &v); err != nil {
		t.Fatal(err)
	}
	// Every child becomes true, leaves too (as the emulator 4.11.2 answers).
	equal(t, "GetShallow", v, map[string]any{"a": true, "b": true, "c": true, "d": true})
	if err := ref.Child("b").GetShallow(ctx, &v); err != nil {
		t.Fatal(err)
	}
	equal(t, "GetShallow of a leaf", v, 2)
}

// keys runs a query and returns the keys in the order the SDK sorts them, and how many
// children the server returned.
func keys(t *testing.T, q *db.Query) ([]string, int) {
	t.Helper()
	ctx := context.Background()
	nodes, err := q.GetOrdered(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ks := make([]string, len(nodes))
	for i, n := range nodes {
		ks[i] = n.Key()
	}
	var raw map[string]any
	if err := q.Get(ctx, &raw); err != nil {
		t.Fatal(err)
	}
	return ks, len(raw)
}

func TestQueries(t *testing.T) {
	ctx := context.Background()
	setRules(t)
	c := client(t, nil)
	scores := c.NewRef("scores")
	if err := scores.Set(ctx, map[string]any{"a": map[string]any{"score": 30}, "b": map[string]any{"score": 10},
		"c": map[string]any{"score": 20}, "d": map[string]any{"score": 5}, "e": map[string]any{"name": "no score"}}); err != nil {
		t.Fatal(err)
	}
	values := c.NewRef("values")
	if err := values.Set(ctx, map[string]any{"x": 3, "y": 1, "z": 2, "w": "text"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		q    *db.Query
		want []string
	}{
		// Children without the value sort first, then by the value.
		{"orderByChild limitToFirst", scores.OrderByChild("score").LimitToFirst(3), []string{"e", "d", "b"}},
		{"orderByChild startAt limitToFirst", scores.OrderByChild("score").StartAt(10).LimitToFirst(2), []string{"b", "c"}},
		{"orderByChild startAt endAt", scores.OrderByChild("score").StartAt(6).EndAt(25), []string{"b", "c"}},
		{"orderByChild equalTo", scores.OrderByChild("score").EqualTo(20), []string{"c"}},
		{"orderByChild limitToLast", scores.OrderByChild("score").LimitToLast(2), []string{"c", "a"}},
		{"orderByKey startAt", scores.OrderByKey().StartAt("c"), []string{"c", "d", "e"}},
		{"orderByKey limitToLast", scores.OrderByKey().LimitToLast(2), []string{"d", "e"}},
		// Numbers sort before strings.
		{"orderByValue", values.OrderByValue(), []string{"y", "z", "x", "w"}},
		{"orderByValue limitToFirst", values.OrderByValue().LimitToFirst(2), []string{"y", "z"}},
		{"orderByValue startAt", values.OrderByValue().StartAt(2).LimitToFirst(2), []string{"z", "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, n := keys(t, tc.q)
			if !reflect.DeepEqual(got, tc.want) || n != len(tc.want) {
				t.Fatalf("keys %v (%d returned), want %v", got, n, tc.want)
			}
		})
	}

	// REST queries need the index (Firebase answers 400 "Index not defined").
	var v any
	err := scores.OrderByChild("name").LimitToFirst(1).Get(ctx, &v)
	if resp := errorutils.HTTPResponse(err); resp == nil || resp.StatusCode != http.StatusBadRequest || !strings.Contains(err.Error(), "Index not defined") {
		t.Fatalf("query without an index: %v", err)
	}
}

func TestAuthOverride(t *testing.T) {
	ctx := context.Background()
	setRules(t)
	admin := client(t, nil)
	if err := admin.NewRef("users/u2").Set(ctx, map[string]any{"name": "two"}); err != nil {
		t.Fatal(err)
	}
	if err := admin.NewRef("public").Set(ctx, map[string]any{"motd": "hello"}); err != nil {
		t.Fatal(err)
	}

	u1 := client(t, &map[string]interface{}{"uid": "u1"})
	mine := u1.NewRef("users/u1")
	if err := mine.Set(ctx, map[string]any{"name": "one"}); err != nil {
		t.Fatalf("u1 writing users/u1: %v", err)
	}
	if err := mine.Update(ctx, map[string]any{"age": 3}); err != nil {
		t.Fatalf("u1 updating users/u1: %v", err)
	}
	equal(t, "users/u1 read by u1", get(t, mine), map[string]any{"name": "one", "age": 3})
	if _, err := u1.NewRef("users/u1/list").Push(ctx, "x"); err != nil {
		t.Fatalf("u1 pushing under users/u1: %v", err)
	}
	if err := u1.NewRef("users/u1/list").Delete(ctx); err != nil {
		t.Fatalf("u1 deleting under users/u1: %v", err)
	}
	equal(t, "public read by u1", get(t, u1.NewRef("public/motd")), "hello")

	var v any
	denied(t, "u1 reading users/u2", u1.NewRef("users/u2").Get(ctx, &v))
	denied(t, "u1 writing users/u2", u1.NewRef("users/u2").Set(ctx, map[string]any{"name": "taken"}))
	denied(t, "u1 updating users/u2", u1.NewRef("users/u2").Update(ctx, map[string]any{"name": "taken"}))
	denied(t, "u1 deleting users/u2", u1.NewRef("users/u2").Delete(ctx))
	_, err := u1.NewRef("users/u2").Push(ctx, "x")
	denied(t, "u1 pushing under users/u2", err)
	denied(t, "u1 reading users", u1.NewRef("users").Get(ctx, &v))
	denied(t, "u1 writing public", u1.NewRef("public/motd").Set(ctx, "defaced"))
	equal(t, "users/u2 after the refused writes", get(t, admin.NewRef("users/u2")), map[string]any{"name": "two"})

	var none map[string]interface{} // auth_variable_override=null: unauthenticated
	anon := client(t, &none)
	equal(t, "public read unauthenticated", get(t, anon.NewRef("public")), map[string]any{"motd": "hello"})
	denied(t, "unauthenticated reading users/u1", anon.NewRef("users/u1").Get(ctx, &v))
	denied(t, "unauthenticated writing users/u1", anon.NewRef("users/u1").Set(ctx, "x"))
	equal(t, "public after the refused write", get(t, admin.NewRef("public/motd")), "hello")
}
