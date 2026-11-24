package ops

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A rolling restart starts the new process while the old one still serves. The Realtime Database
// and the Document Database keep their listeners in memory and run on one process per Postgres
// database (docs/reference/configuration.md): the newcomer serves the rest, answers both with
// UNAVAILABLE and /readyz with 503, and takes over when the old process stops. Storage's data
// directory is locked by one process, so a newcomer with Storage waits before it listens.

// startListening launches `aruma serve` and waits until it answers HTTP at all, ready or not.
func (d *deployment) startListening() time.Duration {
	d.t.Helper()
	d.log = &lockedBuf{}
	if err := d.launch(); err != nil {
		d.t.Fatal(err)
	}
	start := time.Now()
	deadline := start.Add(120 * time.Second)
	defer func() { d.t.Logf("listening after %v", time.Since(start).Round(100*time.Millisecond)) }()
	for time.Now().Before(deadline) {
		select {
		case <-d.done:
			d.t.Fatalf("aruma serve exited: %v\n%s", d.exited, d.log.String())
		default:
		}
		if resp, err := http.Get(d.base() + "/console/v1/status"); err == nil {
			resp.Body.Close()
			return time.Since(start)
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.t.Fatalf("aruma serve does not listen after 120s\n%s", d.log.String())
	return 0
}

func (d *deployment) readyz() int {
	resp, err := http.Get(d.base() + "/readyz")
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// waitReady waits for /readyz to answer 200 and returns how long it took.
func (d *deployment) waitReady(within time.Duration) time.Duration {
	d.t.Helper()
	start := time.Now()
	for time.Since(start) < within {
		if d.readyz() == 200 {
			return time.Since(start)
		}
		time.Sleep(200 * time.Millisecond)
	}
	d.t.Fatalf("not ready %v after the old process stopped\n%s", within, d.log.String())
	return 0
}

// sibling is a second process of the same installation: the same binary, database, Storage
// directory and key, on ports of its own.
func (d *deployment) sibling(bin string) *deployment {
	return &deployment{t: d.t, bin: bin, dbURL: d.dbURL, dir: d.dir, key: d.key, port: freePort(d.t), mport: freePort(d.t),
		extra: d.extra, log: &lockedBuf{}}
}

// rtdbEvents is a Realtime Database listener on one location, over the SDKs' WebSocket
// protocol, which keeps every frame it receives.
type rtdbEvents struct {
	mu     sync.Mutex
	frames []string
}

func listenRTDB(t *testing.T, base, ns, path string) *rtdbEvents {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dial, dcancel := context.WithTimeout(ctx, 10*time.Second)
	defer dcancel()
	ws, _, err := websocket.Dial(dial, "ws"+strings.TrimPrefix(base, "http")+"/.ws?v=5&ns="+ns,
		&websocket.DialOptions{HTTPClient: &http.Client{Transport: &http.Transport{Proxy: nil}}})
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	ev := &rtdbEvents{}
	go func() {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			ev.mu.Lock()
			ev.frames = append(ev.frames, string(b))
			ev.mu.Unlock()
		}
	}()
	q := `{"t":"d","d":{"r":1,"a":"q","b":{"p":"` + path + `","h":""}}}`
	if err := ws.Write(ctx, websocket.MessageText, []byte(q)); err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !ev.saw(`"r":1`) {
		if time.Now().After(deadline) {
			t.Fatalf("listen %s: no answer: %v", path, ev.frames)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return ev
}

func (e *rtdbEvents) saw(s string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, f := range e.frames {
		if strings.Contains(f, s) {
			return true
		}
	}
	return false
}

// rollingFixture is a project with Realtime Database data and a document, on a deployment
// without Storage (Storage's own lock is TestRollingRestartWithStorage's).
func rollingFixture(t *testing.T, d *deployment) (*owner, *project) {
	t.Helper()
	o := createOwner(t, d, "owner@example.com", ownerPassword)
	p := o.newProject("roll-proj")
	o.a.must("PUT", "/.settings/rules.json?ns="+p.NS, bearer(o.pat), `{"rules":{"chat":{".read":true}}}`)
	o.a.must("PUT", rtdbURL(p, "chat/m1", ""), bearer(o.pat), map[string]any{"text": "before"})
	o.a.must("PATCH", docsBase(p)+"/documents/cities/sf", bearer(o.pat), map[string]any{"fields": map[string]any{"n": map[string]any{"integerValue": "1"}}})
	return o, p
}

func TestRollingRestart(t *testing.T) {
	head := headBinary(t)
	old := newDeployment(t, head, "roll")
	old.extra = []string{"ARUMA_STORAGE_ENABLED=false"}
	old.mustRun("", "migrate", "up")
	old.mustStart()
	o, p := rollingFixture(t, old)

	nu := old.sibling(head)
	nu.mustRun("", "migrate", "up") // the deploy migrates first; nothing to do, and no wait
	if took := nu.startListening(); took > 45*time.Second {
		t.Errorf("the newcomer opened its listener after %v: it waits up to 30 seconds for the locks", took)
	}
	na := newAPI(t, nu.base())

	// While the old process serves: the newcomer is not ready, refuses the two services that
	// hold listeners, and serves the rest.
	time.Sleep(time.Second)
	if s := nu.readyz(); s != 503 {
		t.Errorf("/readyz of the newcomer while the old process serves: %d, want 503", s)
	}
	if r := na.do("GET", rtdbURL(p, "chat", ""), bearer(o.pat), nil); r.Status != 503 {
		t.Errorf("the newcomer's Realtime Database while the old process serves: %s", r)
	}
	if r := na.do("GET", docsBase(p)+"/documents/cities/sf", bearer(o.pat), nil); r.Status != 503 {
		t.Errorf("the newcomer's Document Database while the old process serves: %s", r)
	}
	if r := signIn(na, p, "nobody@example.com", "x"); r.Status != 400 {
		t.Errorf("the newcomer's Auth while the old process serves: %s", r)
	}
	if !strings.Contains(nu.log.String(), "another Aruma instance serves rtdb") {
		t.Errorf("the newcomer does not log why it waits:\n%s", nu.log.String())
	}

	// The old process stops; the newcomer takes over and serves what the old one wrote.
	old.stop()
	// Ready means both services: a load balancer must not send traffic to a process that
	// refuses the Document Database.
	stopped := time.Now()
	for {
		ready := nu.readyz() == 200
		docs := na.do("GET", docsBase(p)+"/documents/cities/sf", bearer(o.pat), nil).Status
		if ready && docs != 200 {
			t.Errorf("/readyz is 200 while the Document Database answers %d", docs)
		}
		if ready && docs == 200 {
			break
		}
		if time.Since(stopped) > 90*time.Second {
			t.Fatalf("the newcomer did not take over in 90s\n%s", nu.log.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("the newcomer was ready %v after the old process stopped", time.Since(stopped).Round(100*time.Millisecond))
	if r := na.do("GET", rtdbURL(p, "chat/m1/text", ""), bearer(o.pat), nil); r.Status != 200 || !strings.Contains(string(r.Body), "before") {
		t.Errorf("the Realtime Database after the takeover: %s", r)
	}
	na.must("PUT", rtdbURL(p, "chat/m2", ""), bearer(o.pat), map[string]any{"text": "after"})
	if r := na.do("GET", docsBase(p)+"/documents/cities/sf", bearer(o.pat), nil); r.Status != 200 {
		t.Errorf("the Document Database after the takeover: %s", r)
	}
}

// TestRollingRestartWithStorage: Storage's data directory is locked by one process; the
// newcomer waits for it before it listens, says so in its log, and starts once the old one stops.
func TestRollingRestartWithStorage(t *testing.T) {
	head := headBinary(t)
	old := newDeployment(t, head, "roll")
	old.mustRun("", "migrate", "up")
	old.mustStart()
	createOwner(t, old, "owner@example.com", ownerPassword)

	nu := old.sibling(head)
	nu.log = &lockedBuf{}
	if err := nu.launch(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(nu.log.String(), "waiting for the data directory's lock") && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(nu.log.String(), "waiting for the data directory's lock") {
		t.Errorf("the newcomer does not say it waits for the data directory:\n%s", nu.log.String())
	}
	if _, err := http.Get(nu.base() + "/console/v1/status"); err == nil {
		t.Errorf("the newcomer listens while the old process holds the data directory")
	}
	old.stop()
	took := nu.waitReady(60 * time.Second)
	t.Logf("the newcomer was ready %v after the old process stopped", took.Round(100*time.Millisecond))
}

// TestRollingUpgradeFromPreviousRelease: the earlier release (ARUMA_OLD_BIN) does not take the
// Realtime Database's lock, which this binary added: during a rolling upgrade both processes
// serve the Realtime Database, and a listener on one does not hear a write on the other. The
// operations guide therefore says to stop the old release before starting this one. The test
// pins the behaviour, so the guide stays true.
func TestRollingUpgradeFromPreviousRelease(t *testing.T) {
	oldBin := oldBinary(t)
	old := newDeployment(t, oldBin, "rollup")
	old.extra = []string{"ARUMA_STORAGE_ENABLED=false"}
	old.mustRun("", "migrate", "up")
	old.mustStart()
	o, p := rollingFixture(t, old)
	ev := listenRTDB(t, old.base(), p.NS, "/chat")
	if !ev.saw("before") {
		t.Fatalf("the listener on the earlier release did not get the data: %v", ev.frames)
	}

	nu := old.sibling(headBinary(t))
	nu.mustRun("", "migrate", "up")
	nu.startListening()
	time.Sleep(2 * time.Second)
	na := newAPI(t, nu.base())
	r := na.do("PUT", rtdbURL(p, "chat/m9", ""), bearer(o.pat), map[string]any{"text": "split"})
	if r.Status == 503 {
		t.Fatalf("the new release refuses the Realtime Database beside the earlier one: the earlier release takes the lock after all; update the operations guide and this test")
	}
	time.Sleep(2 * time.Second)
	if ev.saw("split") {
		t.Fatalf("a listener on the earlier release heard a write on the new one: update the operations guide and this test")
	}
	t.Logf("as the operations guide warns: during a rolling upgrade from %s both processes serve the Realtime Database (ready: %d), and a listener on the old one misses a write on the new one",
		strings.TrimSpace(old.mustRun("", "version")), nu.readyz())
}
