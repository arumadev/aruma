package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/services/rtdb/core"
	"github.com/arumadev/aruma/tools/internal/rtdbkit"
)

// A worker is one client. It owns the subtree /c/<key>, so the expected value of that
// subtree follows from its own acknowledged writes alone, whatever the other clients do
// meanwhile; the shared paths (/shared) take only commutative changes (increments, and keys
// added by transactions), so their expected value does not depend on the order the server
// put the clients' writes in either.
type worker struct {
	r    *runner
	id   int
	key  string
	rng  *rand.Rand
	base []string // /c/<key>

	model  any // the expected value of /c/<key>
	nextOp int64
	pushes core.PushIDs

	conn    *rtdbkit.Conn
	session *session
	// pending are the writes sent on a connection that died before their reply, in the
	// order sent: the server applied some prefix of them.
	pending []*op

	// Shared state the worker caused.
	sharedInc     int             // receipt increments known applied
	plainApplied  int             // plain increments known applied
	plainUnknown  int             // plain increments whose outcome is unknown
	txApplied     map[string]bool // transaction keys known applied
	txUnknown     map[string]bool // transaction keys whose outcome is unknown
	sessions      []*session
	writesApplied int64 // writes known applied, for seq
	steps         int

	st      stats
	journal []string
}

// stats are a worker's counts, summed into the run's report.
type stats struct {
	Writes         int64 `json:"writes"`          // writes acknowledged with ok
	REST           int64 `json:"rest"`            // of which over REST
	InFlight       int64 `json:"inFlight"`        // writes whose reply never came (connection died)
	InFlightLanded int64 `json:"inFlightApplied"` // of which the server had applied
	Ambiguous      int64 `json:"ambiguous"`       // in-flight writes only the shared state resolves
	Stale          int64 `json:"datastale"`       // transaction retries
	Reconnects     int64 `json:"reconnects"`
	Disconnects    int64 `json:"voluntaryDisconnects"`
	Verifies       int64 `json:"verifies"`
}

func (s *stats) add(o stats) {
	s.Writes += o.Writes
	s.REST += o.REST
	s.InFlight += o.InFlight
	s.InFlightLanded += o.InFlightLanded
	s.Ambiguous += o.Ambiguous
	s.Stale += o.Stale
	s.Reconnects += o.Reconnects
	s.Disconnects += o.Disconnects
	s.Verifies += o.Verifies
}

// op is one write and what it does when the server applies it.
type op struct {
	n    int64
	kind string
	// effects are what it writes in the worker's subtree, paths relative to it, in order.
	effects []effect
	// invisible ops change only shared state: the subtree cannot tell whether they ran.
	plainInc bool   // +1 on /shared/plain
	txKey    string // a key added to /shared/tx/<bucket>, as "<bucket>/<key>"
	// sharedInc is +1 on /shared/inc, with the receipt in effects.
	sharedInc bool
	// postValue is a REST POST's value; its key is known only from the reply.
	postValue any
	// How it is sent: action and body over the WebSocket, or method, path and body over REST.
	action string
	body   any
	method string
	path   string
	query  url.Values
	rbody  any
}

type effect struct {
	path  []string
	value any
}

func (o *op) visible() bool { return len(o.effects) > 0 || o.postValue != nil }

// session is one connection's onDisconnect registrations and how the connection ended.
type session struct {
	key   string
	epoch int64
	// entries are the presence entries registered, by key under /presence/<worker>: true
	// once acknowledged, false while unknown.
	entries map[string]any
	acked   map[string]bool
	// cancelled is the entry registered then cancelled with oc: it must never appear once
	// the cancel is acknowledged.
	cancelled    string
	cancelAcked  bool
	ended        string // "", "close", "kill", "term", "drop"
	closeEpoch   int64  // the server epoch a voluntary close happened in
	confirmed    bool   // a voluntary close whose writes were seen to land
	waitedFailed bool
}

func newWorker(r *runner, id int) *worker {
	key := fmt.Sprintf("c%d", id)
	return &worker{
		r: r, id: id, key: key, base: []string{"c", key},
		rng:       rand.New(rand.NewPCG(uint64(r.seed), uint64(id)+1)),
		txApplied: map[string]bool{}, txUnknown: map[string]bool{},
	}
}

func (w *worker) logf(format string, args ...any) {
	if len(w.journal) < 200_000 {
		w.journal = append(w.journal, fmt.Sprintf("%s %s", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...)))
	}
}

func (w *worker) opID() string {
	w.nextOp++
	return fmt.Sprintf("%s-%d", w.key, w.nextOp)
}

// run drives the worker until the runner stops it.
func (w *worker) run(ctx context.Context) {
	for !w.r.stopping.Load() && ctx.Err() == nil {
		if w.conn == nil {
			if err := w.connect(ctx); err != nil {
				return
			}
			continue
		}
		w.step(ctx)
	}
	if ctx.Err() != nil {
		return
	}
	// Settle: reconnect if needed (which resolves what is pending), then end the session
	// normally, so its onDisconnect writes run, and wait until they land.
	for w.conn == nil {
		if err := w.connect(ctx); err != nil {
			return
		}
	}
	w.disconnect(ctx)
}

// connect waits for the server, dials, resolves what was pending, and registers the new
// session's onDisconnect writes.
func (w *worker) connect(ctx context.Context) error {
	for {
		epoch, err := w.r.waitUp(ctx)
		if err != nil {
			return err
		}
		c, err := rtdbkit.Dial(ctx, w.r.srv.URL, w.r.ns, rtdbkit.DialOptions{HTTPClient: w.r.wsHTTP})
		if err != nil {
			sleep(ctx, 20*time.Millisecond)
			continue
		}
		if w.r.epoch() != epoch {
			// A restart came between: this may be the next server, not epoch's.
			c.CloseNow()
			continue
		}
		w.conn = c
		w.st.Reconnects++
		if !w.resolve(ctx) {
			w.lost()
			continue
		}
		if !w.register(ctx, epoch) {
			w.lost()
			continue
		}
		return nil
	}
}

// lost handles a connection that died under the worker: its session ended with the server.
func (w *worker) lost() {
	if w.conn == nil {
		return
	}
	w.conn.CloseNow()
	w.conn = nil
	if s := w.session; s != nil {
		if s.ended = w.r.stopOf(s.epoch); s.ended == "" {
			s.ended = "drop"
			w.r.violation("%s: connection of session %s dropped while the server ran", w.key, s.key)
		}
		w.session = nil
	}
}

// register opens a session record and registers its onDisconnect writes: a set, an update,
// and a set cancelled again with oc.
func (w *worker) register(ctx context.Context, epoch int64) bool {
	s := &session{key: fmt.Sprintf("s%d", len(w.sessions)+1), epoch: epoch, entries: map[string]any{}, acked: map[string]bool{}}
	w.sessions = append(w.sessions, s)
	w.session = s
	pres := "/presence/" + w.key
	entryO := map[string]any{"op": w.key + "-" + s.key, "via": "o"}
	entryM := map[string]any{"op": w.key + "-" + s.key, "via": "om"}
	s.entries[s.key], s.entries[s.key+"m"] = entryO, entryM
	s.cancelled = s.key + "x"
	reqs := []struct {
		action string
		body   any
		entry  string
	}{
		{"o", rtdbkit.Put(pres+"/"+s.key, entryO, nil), s.key},
		{"om", map[string]any{"p": pres, "d": map[string]any{s.key + "m": entryM}}, s.key + "m"},
		{"o", rtdbkit.Put(pres+"/"+s.cancelled, map[string]any{"cancelled": true}, nil), ""},
		{"oc", map[string]any{"p": pres + "/" + s.cancelled}, ""},
	}
	for i, q := range reqs {
		rep, err := w.conn.Call(ctx, q.action, q.body)
		if err != nil {
			return false
		}
		if !rep.OK() {
			w.r.violation("%s: %s %v refused: %s %s", w.key, q.action, q.body, rep.Status, rep.Data)
			return false
		}
		if q.entry != "" {
			s.acked[q.entry] = true
		}
		if i == 3 {
			s.cancelAcked = true
		}
	}
	return true
}

// disconnect closes the connection as a client going offline does. The server must then run
// the session's onDisconnect writes; the worker reconnects and waits to see them.
func (w *worker) disconnect(ctx context.Context) {
	s := w.session
	s.closeEpoch = w.r.epoch()
	_ = w.conn.Close()
	w.conn, w.session = nil, nil
	s.ended = "close"
	w.st.Disconnects++
	if s.closeEpoch != w.r.epoch() {
		return
	}
	// Watch for the writes over REST, so that no new session is needed to see them.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		got, err := w.r.restGet(ctx, "/presence/"+w.key+"/"+s.key)
		if err == nil && got != nil {
			s.confirmed = true
			return
		}
		if s.closeEpoch != w.r.epoch() {
			return // the server went down meanwhile: the writes may be lost with it
		}
		sleep(ctx, 20*time.Millisecond)
	}
	if s.closeEpoch == w.r.epoch() && ctx.Err() == nil {
		s.waitedFailed = true
		w.r.violation("%s: onDisconnect writes of session %s did not land within 10 s of a normal close", w.key, s.key)
	}
}

// step runs one operation, or a burst of writes sent back to back.
func (w *worker) step(ctx context.Context) {
	switch x := w.rng.IntN(1000); {
	case x < 8:
		w.disconnect(ctx)
		return
	case x < 70:
		w.transaction(ctx, false)
	case x < 130:
		w.transaction(ctx, true)
	case x < 230:
		w.rest(ctx)
	default:
		w.burst(ctx)
	}
	if w.steps++; w.steps%40 == 0 && w.conn != nil {
		if !w.verify(ctx) {
			w.lost()
		}
	}
}

// burst sends 1 to 4 writes without waiting, then waits for the replies in order.
func (w *worker) burst(ctx context.Context) {
	n := []int{1, 1, 1, 2, 3, 4}[w.rng.IntN(6)]
	model := w.model
	var ops []*op
	for len(ops) < n {
		for _, o := range w.genWS(model) {
			for _, e := range o.effects {
				model = setAt(model, e.path, e.value)
			}
			ops = append(ops, o)
		}
	}
	chans := make([]<-chan rtdbkit.Reply, len(ops))
	for i, o := range ops {
		ch, err := w.conn.Send(o.action, o.body)
		if err != nil {
			// The connection had ended: nothing from here on was sent.
			ops = ops[:i]
			chans = chans[:i]
			break
		}
		chans[i] = ch
	}
	for i, o := range ops {
		rep, err := rtdbkit.Await(ctx, chans[i])
		switch {
		case errors.Is(err, rtdbkit.ErrClosed):
			// This and every later reply are lost; the server applied a prefix.
			w.pending = append(w.pending, ops[i:]...)
			w.st.InFlight += int64(len(ops) - i)
			for _, p := range ops[i:] {
				w.logf("in flight: %s %s", p.kind, short(p.body))
			}
			w.lost()
			return
		case err != nil:
			return
		case !rep.OK():
			w.r.violation("%s: %s %s refused: %s %s", w.key, o.kind, short(o.body), rep.Status, rep.Data)
		default:
			w.applied(o)
		}
	}
}

// applied records an acknowledged (or resolved) write.
func (w *worker) applied(o *op) {
	for _, e := range o.effects {
		w.model = setAt(w.model, e.path, e.value)
	}
	w.st.Writes++
	w.writesApplied++
	if o.sharedInc {
		w.sharedInc++
	}
	if o.plainInc {
		w.plainApplied++
	}
	if o.txKey != "" {
		w.txApplied[o.txKey] = true
	}
	w.logf("ok %s %s", o.kind, short(o.body))
}

// keyFrom picks one of n keys under a prefix: few, so writes overwrite each other.
func (w *worker) keyFrom(prefix string, n int) string {
	return fmt.Sprintf("%s%d", prefix, w.rng.IntN(n))
}

// value makes a new value that carries the operation's id, so that every write is visible.
func (w *worker) value(id string) any {
	switch w.rng.IntN(5) {
	case 0:
		return "v-" + id
	case 1:
		return float64(w.id*10_000_000) + float64(w.nextOp)
	case 2:
		return map[string]any{"op": id, "b": w.rng.IntN(2) == 0}
	case 3:
		return map[string]any{"op": id, "nested": map[string]any{"x": float64(w.rng.IntN(100)), "y": "z"}}
	default:
		if w.rng.IntN(20) == 0 {
			// Bigger than one frame (16384 characters), so the message is split.
			return map[string]any{"op": id, "big": strings.Repeat("ء", 9000+w.rng.IntN(20000))}
		}
		return map[string]any{"op": id}
	}
}

// existing returns a key that exists under prefix in model, or "".
func (w *worker) existing(model any, prefix string) string {
	m, _ := getAt(model, []string{prefix}).(map[string]any)
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys[w.rng.IntN(len(keys))]
}

func (w *worker) abs(rel ...string) string {
	return "/" + strings.Join(append(append([]string{}, w.base...), rel...), "/")
}

// genWS makes the next WebSocket write, or two for a delete followed by a write beneath it.
// model is the subtree as it will be once the writes before have run.
func (w *worker) genWS(model any) []*op {
	id := w.opID()
	inc := func(d float64) map[string]any { return map[string]any{".sv": map[string]any{"increment": d}} }
	num := func(p []string) float64 { f, _ := getAt(model, p).(float64); return f }
	switch x := w.rng.IntN(100); {
	case x < 20: // a set
		k := w.keyFrom("k", 8)
		v := w.value(id)
		return []*op{{kind: "set", action: "p", body: rtdbkit.Put(w.abs("s", k), v, nil), effects: []effect{{[]string{"s", k}, v}}}}
	case x < 32: // a multi-path update at the root, with a null and a deep path
		k1, k2, k3 := w.keyFrom("k", 6), w.keyFrom("k", 6), w.keyFrom("k", 6)
		v1, v3 := w.value(id), w.value(id)
		d := map[string]any{}
		var eff []effect
		add := func(rel []string, v any) {
			p := strings.Join(append(append([]string{}, w.base...), rel...), "/")
			for q := range d {
				if strings.HasPrefix(q+"/", p+"/") || strings.HasPrefix(p+"/", q+"/") {
					return // overlapping paths are refused; keep the first
				}
			}
			d[p] = v
			eff = append(eff, effect{rel, v})
		}
		add([]string{"receipt"}, id)
		add([]string{"u", k1}, v1)
		add([]string{"u", k2}, nil)
		add([]string{"u", k3, "deep", "x"}, v3)
		return []*op{{kind: "update-root", action: "m", body: map[string]any{"p": "/", "d": d}, effects: sortEffects(eff)}}
	case x < 44: // an update below the subtree, with nulls
		k1, k2 := w.keyFrom("k", 6), w.keyFrom("k", 6)
		d := map[string]any{"_r": id}
		eff := []effect{{[]string{"m", "_r"}, id}}
		if k1 != k2 {
			v := w.value(id)
			d[k1+"/y"] = v
			d[k2] = nil
			eff = append(eff, effect{[]string{"m", k1, "y"}, v}, effect{[]string{"m", k2}, nil})
		}
		return []*op{{kind: "update", action: "m", body: map[string]any{"p": w.abs("m"), "d": d}, effects: sortEffects(eff)}}
	case x < 54: // delete a node, then at once write beneath it
		k := w.existing(model, "d")
		if k == "" {
			k = w.keyFrom("k", 4)
			v := w.value(id)
			return []*op{{kind: "set", action: "p", body: rtdbkit.Put(w.abs("d", k), v, nil), effects: []effect{{[]string{"d", k}, v}}}}
		}
		id2 := w.opID()
		v := w.value(id2)
		return []*op{
			{kind: "delete", action: "p", body: rtdbkit.Put(w.abs("d", k), nil, nil), effects: []effect{{[]string{"d", k}, nil}}},
			{kind: "set-beneath", action: "p", body: rtdbkit.Put(w.abs("d", k, "x", "y"), v, nil), effects: []effect{{[]string{"d", k, "x", "y"}, v}}},
		}
	case x < 64: // increment a counter of the worker's own
		k := w.keyFrom("k", 4)
		d := float64(1 + w.rng.IntN(20))
		if w.rng.IntN(4) == 0 {
			d = -d
		}
		return []*op{{kind: "increment", action: "p", body: rtdbkit.Put(w.abs("n", k), inc(d), nil),
			effects: []effect{{[]string{"n", k}, num([]string{"n", k}) + d}}}}
	case x < 70: // increments inside an update
		k1, k2 := w.keyFrom("k", 4), w.keyFrom("k", 4)
		if k1 == k2 {
			k2 = k1 + "b"
		}
		return []*op{{kind: "increment-update", action: "m", body: map[string]any{"p": w.abs("n"), "d": map[string]any{k1: inc(1), k2: inc(2)}},
			effects: sortEffects([]effect{{[]string{"n", k1}, num([]string{"n", k1}) + 1}, {[]string{"n", k2}, num([]string{"n", k2}) + 2}})}}
	case x < 80: // a shared increment with a receipt, in one update at the root
		return []*op{{kind: "shared-increment", action: "m", sharedInc: true,
			body:    map[string]any{"p": "/", "d": map[string]any{"shared/inc": inc(1), "c/" + w.key + "/receipt": id}},
			effects: []effect{{[]string{"receipt"}, id}}}}
	case x < 86: // a shared increment on its own: nothing in the subtree records it
		return []*op{{kind: "plain-increment", action: "p", plainInc: true, body: rtdbkit.Put("/shared/plain", inc(1), nil)}}
	default: // a child with a push id, made as the SDKs make them
		k := w.pushes.Next(time.Now())
		v := w.value(id)
		return []*op{{kind: "push", action: "p", body: rtdbkit.Put(w.abs("push", k), v, nil), effects: []effect{{[]string{"push", k}, v}}}}
	}
}

// sortEffects orders an update's children as the server applies them; as they never
// overlap, any order gives the same result, but the journal reads better.
func sortEffects(e []effect) []effect {
	slices.SortFunc(e, func(a, b effect) int { return strings.Compare(strings.Join(a.path, "/"), strings.Join(b.path, "/")) })
	return e
}

// transaction runs a transaction as the SDKs do: read the value and its hash, then put the
// new value conditional on that hash, again on datastale. Shared ones contend with every
// other worker's.
func (w *worker) transaction(ctx context.Context, shared bool) {
	id := w.opID()
	var path string
	var rel []string
	if shared {
		// New buckets each epoch keep the objects small.
		path = fmt.Sprintf("/shared/tx/e%db%d", w.r.epoch(), w.rng.IntN(3))
	} else {
		rel = []string{"t", w.keyFrom("k", 3)}
		path = w.abs(rel...)
	}
	for attempt := 0; attempt < 50; attempt++ {
		rep, err := w.conn.Call(ctx, "g", rtdbkit.Get(path))
		if err != nil {
			w.lost()
			return
		}
		if !rep.OK() {
			w.r.violation("%s: get %s: %s %s", w.key, path, rep.Status, rep.Data)
			return
		}
		cur, err := decode(rep.Data)
		if err != nil {
			w.r.violation("%s: get %s: %v", w.key, path, err)
			return
		}
		hash, err := hashOf(path, rep.Data)
		if err != nil {
			w.r.violation("%s: hash of %s: %v", w.key, path, err)
			return
		}
		o := &op{kind: "transaction", action: "p"}
		var next any
		if shared {
			o.kind = "shared-transaction"
			m, _ := clone(cur).(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			m[id] = true
			next = m
			o.txKey = path[len("/shared/tx/"):] + "/" + id
		} else {
			if !equal(cur, getAt(w.model, rel)) {
				w.r.violation("%s: %s reads %s, model %s", w.key, path, short(cur), short(getAt(w.model, rel)))
			}
			f := 0.0
			if m, ok := cur.(map[string]any); ok {
				f, _ = m["v"].(float64)
			}
			next = map[string]any{"v": f + 1, "op": id}
			o.effects = []effect{{rel, next}}
		}
		o.body = rtdbkit.Put(path, next, &hash)
		rep, err = w.conn.Call(ctx, "p", o.body)
		switch {
		case errors.Is(err, rtdbkit.ErrClosed):
			w.pending = append(w.pending, o)
			w.st.InFlight++
			w.logf("in flight: %s %s", o.kind, short(o.body))
			w.lost()
			return
		case err != nil:
			return
		case rep.Status == "datastale":
			if !shared {
				w.r.violation("%s: transaction on its own path %s: datastale", w.key, path)
				return
			}
			w.st.Stale++
			continue
		case !rep.OK():
			w.r.violation("%s: %s: %s %s", w.key, o.kind, rep.Status, rep.Data)
			return
		}
		w.applied(o)
		return
	}
}

// hashOf is the hash an SDK sends with a transaction's put: of the value it read at path.
func hashOf(path string, data json.RawMessage) (string, error) {
	p, err := core.ParsePath(path)
	if err != nil {
		return "", err
	}
	n, err := core.ParseData(data, p, 10<<20)
	if err != nil {
		return "", err
	}
	return core.Hash(n), nil
}

// rest runs one write over REST: PUT, PATCH, DELETE or POST.
func (w *worker) rest(ctx context.Context) {
	id := w.opID()
	o := &op{}
	switch x := w.rng.IntN(4); x {
	case 0:
		k := w.keyFrom("k", 6)
		v := w.value(id)
		o.kind, o.method, o.path, o.rbody = "rest-put", http.MethodPut, w.abs("r", k), v
		o.effects = []effect{{[]string{"r", k}, v}}
		if w.rng.IntN(2) == 0 {
			o.query = url.Values{"print": {"silent"}}
		}
	case 1:
		k1, k2 := w.keyFrom("k", 4), w.keyFrom("k", 4)
		v := w.value(id)
		d := map[string]any{"_r": id, "a/" + k1: v}
		eff := []effect{{[]string{"rp", "_r"}, id}, {[]string{"rp", "a", k1}, v}}
		if k2 != k1 {
			d[k2] = nil
			eff = append(eff, effect{[]string{"rp", k2}, nil})
		}
		o.kind, o.method, o.path, o.rbody, o.effects = "rest-patch", http.MethodPatch, w.abs("rp"), d, sortEffects(eff)
	case 2:
		k := w.existing(w.model, "r")
		if k == "" {
			w.rest(ctx)
			return
		}
		o.kind, o.method, o.path = "rest-delete", http.MethodDelete, w.abs("r", k)
		o.effects = []effect{{[]string{"r", k}, nil}}
	case 3:
		v := map[string]any{"op": id, "post": true}
		o.kind, o.method, o.path, o.rbody, o.postValue = "rest-post", http.MethodPost, w.abs("rpush"), v, v
	}
	o.body = map[string]any{"method": o.method, "path": o.path, "body": o.rbody}
	status, b, err := w.r.rest.Do(ctx, o.method, o.path, o.query, o.rbody)
	switch {
	case err != nil:
		w.pending = append(w.pending, o)
		w.st.InFlight++
		w.logf("in flight: %s %s", o.kind, short(o.body))
		// Nothing more may be sent before this write is settled on a fresh connection.
		w.lost()
		return
	case status == http.StatusServiceUnavailable && w.r.stopping.Load():
		return
	case status != http.StatusOK && status != http.StatusNoContent:
		if status >= 500 || status == http.StatusServiceUnavailable {
			// A server shutting down (SIGTERM) answers 503: the write did not run.
			w.pending = append(w.pending, o)
			w.st.InFlight++
			w.logf("in flight (%d): %s %s", status, o.kind, short(o.body))
			w.lost()
			return
		}
		w.r.violation("%s: %s %s: %d %s", w.key, o.method, o.path, status, b)
		return
	}
	if o.postValue != nil {
		var name struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &name) != nil || name.Name == "" {
			w.r.violation("%s: POST %s answered %s", w.key, o.path, b)
			return
		}
		o.effects = []effect{{[]string{"rpush", name.Name}, o.postValue}}
	}
	w.st.REST++
	w.applied(o)
}

// resolve settles the writes whose replies were lost, on a fresh connection to a server
// started after they were sent: it reads the subtree and finds how many of them the server
// applied, which must be a prefix. Writes that change only shared state are settled at the
// end from that state. false when the connection died meanwhile.
func (w *worker) resolve(ctx context.Context) bool {
	if len(w.pending) == 0 {
		return true
	}
	got, ok := w.read(ctx)
	if !ok {
		return false
	}
	// A POST whose reply was lost: its key is the child of rpush holding its value.
	for _, o := range w.pending {
		if o.postValue != nil && len(o.effects) == 0 {
			m, _ := getAt(got, []string{"rpush"}).(map[string]any)
			for k, v := range m {
				if getAt(w.model, []string{"rpush", k}) == nil && equal(v, o.postValue) {
					o.effects = []effect{{[]string{"rpush", k}, o.postValue}}
				}
			}
		}
	}
	// One not found did not run: a key no read can show stands for it.
	for _, o := range w.pending {
		if o.postValue != nil && len(o.effects) == 0 {
			o.effects = []effect{{[]string{"rpush", "(not found)"}, o.postValue}}
		}
	}
	// Which prefixes of the pending writes give the subtree read?
	model := w.model
	var match []int
	for i := 0; ; i++ {
		if equal(model, got) {
			match = append(match, i)
		}
		if i == len(w.pending) {
			break
		}
		for _, e := range w.pending[i].effects {
			model = setAt(model, e.path, e.value)
		}
	}
	if len(match) == 0 {
		w.r.violation("%s: after the restart the subtree matches no prefix of the %d writes in flight: %s",
			w.key, len(w.pending), diff(w.model, got))
		w.logf("resolve failed: got %s", short(got))
		w.model = got // carry on from what is there, so one fault is reported once
		w.pending = nil
		return true
	}
	lo, hi := match[0], match[len(match)-1]
	for i := lo; i < hi; i++ {
		if w.pending[i].visible() {
			w.r.violation("%s: in-flight write %s is ambiguous", w.key, w.pending[i].kind)
		}
	}
	for i, o := range w.pending {
		switch {
		case i < lo:
			w.applied(o)
			w.st.InFlightLanded++
			w.logf("in flight, applied: %s", o.kind)
		case i < hi:
			// Only shared state can tell.
			w.st.Ambiguous++
			if o.plainInc {
				w.plainUnknown++
			}
			if o.txKey != "" {
				w.txUnknown[o.txKey] = true
			}
			w.logf("in flight, unknown: %s", o.kind)
		default:
			w.logf("in flight, not applied: %s", o.kind)
		}
	}
	w.pending = nil
	return true
}

// read reads the worker's subtree; false when the connection died.
func (w *worker) read(ctx context.Context) (any, bool) {
	rep, err := w.conn.Call(ctx, "g", rtdbkit.Get(w.abs()))
	if err != nil {
		return nil, false
	}
	if !rep.OK() {
		w.r.violation("%s: read: %s %s", w.key, rep.Status, rep.Data)
		return nil, false
	}
	v, err := decode(rep.Data)
	if err != nil {
		w.r.violation("%s: read: %v", w.key, err)
		return nil, false
	}
	return v, true
}

// verify compares the subtree with the model now, so a divergence is caught near its cause.
func (w *worker) verify(ctx context.Context) bool {
	got, ok := w.read(ctx)
	if !ok {
		return false
	}
	w.st.Verifies++
	if !equal(w.model, got) {
		w.r.violation("%s: the subtree differs from its acknowledged writes: %s", w.key, diff(w.model, got))
		w.model = got
	}
	return true
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
