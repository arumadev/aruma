package acceptance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/store/storetest"
)

// outageProbe is one request a client makes while Postgres is frozen.
type outageProbe struct {
	name string
	// do sends the request and returns its HTTP status, or for a WebSocket write "ok" or
	// "failed: ..." (a status other than ok, or the connection closed).
	do func(c *http.Client) string
}

// outageProbes are one call on every service and surface that reaches the database.
func outageProbes(h *hardEnv, p *hardProject, ws *wsClient, n int) []outageProbe {
	key, tok := p.key, p.idToken
	req := func(c *http.Client, method, target, ctype, body string, header map[string]string) string {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r, err := http.NewRequest(method, h.URL+target, rd)
		if err != nil {
			return err.Error()
		}
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		for k, v := range header {
			r.Header.Set(k, v)
		}
		resp, err := c.Do(r)
		if err != nil {
			return "no answer: " + err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 500 {
			return fmt.Sprintf("%d %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		return fmt.Sprint(resp.StatusCode)
	}
	bearer := map[string]string{"Authorization": "Bearer " + tok}
	firebase := map[string]string{"Authorization": "Firebase " + tok}
	probes := []outageProbe{
		{"auth signUp", func(c *http.Client) string {
			return req(c, "POST", itk+"accounts:signUp?key="+key, "application/json",
				fmt.Sprintf(`{"email":"outage%d@example.com","password":"secret-1","returnSecureToken":true}`, n), nil)
		}},
		{"auth signInWithPassword", func(c *http.Client) string {
			return req(c, "POST", itk+"accounts:signInWithPassword?key="+key, "application/json",
				`{"email":"alice@example.com","password":"secret-alice","returnSecureToken":true}`, nil)
		}},
		{"auth token refresh", func(c *http.Client) string {
			return req(c, "POST", sts+"?key="+key, "application/x-www-form-urlencoded",
				url.Values{"grant_type": {"refresh_token"}, "refresh_token": {p.refresh}}.Encode(), nil)
		}},
		{"documents commit", func(c *http.Client) string {
			return req(c, "PATCH", hardDocPath(p.id), "application/json", fmt.Sprintf(`{"fields":{"n":{"integerValue":"%d"}}}`, n), bearer)
		}},
		{"documents read", func(c *http.Client) string {
			return req(c, "GET", hardDocPath(p.id), "", "", bearer)
		}},
		{"rtdb rest write", func(c *http.Client) string {
			return req(c, "PUT", fmt.Sprintf("/outage/%d.json?ns=%s&auth=%s", n, p.ns, url.QueryEscape(tok)), "application/json", fmt.Sprint(n), nil)
		}},
		{"storage upload", func(c *http.Client) string {
			return req(c, "POST", fmt.Sprintf("/v0/b/%s/o?name=outage%d.txt", p.bucket, n), "text/plain", "outage", firebase)
		}},
		{"storage metadata", func(c *http.Client) string {
			return req(c, "GET", "/v0/b/"+p.bucket+"/o/secret.txt", "", "", firebase)
		}},
	}
	if ws != nil {
		probes = append(probes, outageProbe{"rtdb websocket write", func(*http.Client) string {
			r := ws.requestQuiet("p", map[string]any{"p": fmt.Sprintf("/outage/ws%d", n), "d": n})
			rep, err := ws.awaitQuiet(r, time.Minute)
			if err != nil {
				return "failed: closed: " + err.Error()
			}
			if rep.Status != "ok" {
				return "failed: " + rep.Status
			}
			return "ok"
		}})
	}
	return probes
}

// While Postgres answers nothing (a paused container, a stalled disk, a partition that keeps TCP
// open), every service answers within database_timeout, and when it cannot reach the database it
// answers 503, which clients retry, instead of holding the request for as long as the freeze
// lasts. When Postgres answers again the server serves at once, with no goroutine, pool
// connection or Postgres session left behind by the requests that timed out.
func TestDatabaseFreezeAnswersUnavailableWithinTheTimeout(t *testing.T) {
	const timeout = 2 * time.Second
	var proxy *storetest.FreezeProxy
	h := newHardEnvOn(t, map[string]string{"ARUMA_DATABASE_TIMEOUT": timeout.String()}, func(dbURL string) string {
		var poolURL string
		proxy, poolURL = storetest.NewFreezeProxy(t, dbURL)
		return poolURL + "&pool_max_conns=8"
	})
	p := h.a
	h.open(p, "outage")
	ws := h.dial(p.ns, dialOpts{})
	ws.call("auth", map[string]any{"cred": p.idToken}).mustOK(t)
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

	run := func(round int, probes []outageProbe) map[string][]string {
		var mu sync.Mutex
		got := map[string][]string{}
		var wg sync.WaitGroup
		for _, pr := range probes {
			wg.Go(func() {
				start := time.Now()
				res := pr.do(client)
				took := time.Since(start)
				mu.Lock()
				got[pr.name] = append(got[pr.name], res)
				mu.Unlock()
				if round == 1 {
					t.Logf("frozen: %s answered %q after %v", pr.name, res, took.Round(time.Millisecond))
				}
				if took > 2*timeout+2*time.Second {
					t.Errorf("round %d: %s answered after %v (%s), want within about %v", round, pr.name, took.Round(time.Millisecond), res, timeout)
				}
			})
		}
		wg.Wait()
		return got
	}
	// Every probe works on a healthy database.
	for name, res := range run(0, outageProbes(h, p, ws, 0)) {
		if res[0] != "200" && res[0] != "ok" {
			t.Fatalf("before the freeze, %s: %s", name, res[0])
		}
	}
	sessions := func() int {
		conn, err := pgx.Connect(context.Background(), h.DB)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(context.Background())
		var n int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid()`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	time.Sleep(200 * time.Millisecond)
	baseGoroutines, baseSessions := runtime.NumGoroutine(), sessions()

	proxy.Freeze()
	frozen := time.Now()
	// Twice the pool's size in requests, so some wait for a connection rather than a statement.
	var probes []outageProbe
	for i := 1; i <= 2; i++ {
		probes = append(probes, outageProbes(h, p, nil, i)...)
	}
	probes = append(probes, outageProbes(h, p, ws, 3)[8:]...) // and the WebSocket write
	unavailable := 0
	for name, results := range run(1, probes) {
		for _, res := range results {
			switch {
			case strings.HasPrefix(res, "503"), strings.HasPrefix(res, "failed: unavailable"), strings.HasPrefix(res, "failed: closed"):
				unavailable++
			case res == "200" || res == "ok":
				// Served without the database (a cache).
			default:
				t.Errorf("while the database is frozen, %s: %s, want 503", name, res)
			}
		}
	}
	if unavailable == 0 {
		t.Error("no request reached the frozen database")
	}
	t.Logf("%d requests answered UNAVAILABLE within %v of the freeze", unavailable, time.Since(frozen).Round(time.Millisecond))

	proxy.Thaw()
	thawed := time.Now()
	ws = h.dial(p.ns, dialOpts{}) // the old one may have been closed
	ws.call("auth", map[string]any{"cred": p.idToken}).mustOK(t)
	for {
		failed := ""
		for name, res := range run(2, outageProbes(h, p, ws, 4)) {
			if res[0] != "200" && res[0] != "ok" {
				failed += fmt.Sprintf(" %s: %s;", name, res[0])
			}
		}
		if failed == "" {
			break
		}
		if time.Since(thawed) > 10*time.Second {
			t.Fatalf("10 s after the database answers again:%s", failed)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("every service serves %v after the database answers again", time.Since(thawed).Round(time.Millisecond))

	// What the freeze left behind: goroutines, the pool's connections, Postgres sessions.
	pool := h.Pool
	deadline := time.Now().Add(15 * time.Second)
	for {
		g, s, open := runtime.NumGoroutine(), sessions(), pool.Stat().TotalConns()
		// The new WebSocket (two goroutines on each side) is the only lasting addition.
		if g <= baseGoroutines+8 && s <= baseSessions+2 && open <= pool.Config().MaxConns {
			t.Logf("goroutines %d -> %d, sessions %d -> %d, pool connections %d", baseGoroutines, g, baseSessions, s, open)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the freeze: goroutines %d (%d before), Postgres sessions %d (%d before), pool connections %d",
				g, baseGoroutines, s, baseSessions, open)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
