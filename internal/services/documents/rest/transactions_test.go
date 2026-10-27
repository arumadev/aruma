package rest_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
)

// The transactions over REST, against production's recordings: the answers to an id production
// never issued and to one already committed (f0-errors.json txn_unknown.*, txn_reused.*), and the
// contention scenarios (f0-contention.json).

func readCorpusFile(t *testing.T, name string, into any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// TestTransactionErrorsReplay replays the REST transaction cases of f0-errors.json in their
// order. The recorded id of txn_reused is the one its begin case answered; ours stands for it.
func TestTransactionErrorsReplay(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	var f corpusFile
	readCorpusFile(t, "f0-errors.json", &f)
	// notSent are the cases of a later phase.
	notSent := map[string]string{"rest.txn_unknown.runQuery": "RunQuery is F3"}
	var begun string
	compared := 0
	for _, c := range f.Cases {
		if c.Transport != "rest" || !strings.Contains(c.ID, ".txn_") || notSent[c.ID] != "" {
			continue
		}
		path := "/v1/" + strings.ReplaceAll(c.Request.Path, "{project}", apitest.Project)
		body := string(c.Request.Body)
		if strings.HasPrefix(c.ID, "rest.txn_reused.commit") {
			var req struct {
				Transaction string `json:"transaction"`
			}
			if err := json.Unmarshal(c.Request.Body, &req); err != nil || begun == "" {
				t.Fatalf("%s: %v, begun %q", c.ID, err, begun)
			}
			body = strings.ReplaceAll(body, req.Transaction, begun)
		}
		code, _, got := h.do(t, c.Request.Method, path, body, tok)
		if c.ID == "rest.txn_reused.begin" {
			begun = transactionOf(got)
		}
		compared++
		if code != c.Response.HTTP || !sameJSON(t, normalise(t, got), normalise(t, c.Response.Body)) {
			t.Errorf("%s: %s %s %s\n got %d %s\nwant %d %s", c.ID, c.Request.Method, path, body, code, got, c.Response.HTTP, c.Response.Body)
		}
	}
	if compared != 6 {
		t.Errorf("compared %d cases", compared)
	}
}

// contentionCase is a run of a scenario of f0-contention.json.
type contentionCase struct {
	ID       string `json:"id"`
	Scenario string `json:"scenario"`
	Pattern  string `json:"pattern"`
	Events   []struct {
		Actor   string `json:"actor"`
		N       int    `json:"n"`
		Op      string `json:"op"`
		Outcome string `json:"outcome"`
		HTTP    int    `json:"http"`
		Request struct {
			Method string          `json:"method"`
			Path   string          `json:"path"`
			Body   json.RawMessage `json:"body"`
		} `json:"request"`
		Error *struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	} `json:"events"`
	Final map[string]struct {
		HTTP   int     `json:"http"`
		Writer string  `json:"writer"`
		Step   *string `json:"step"`
	} `json:"final"`
}

// contentionScale is how much faster than production the replay runs: production's 20 s lock
// wait is 1 s here, and the recorder's 5 s (25 s in s6) before it treats a request as blocked is
// 250 ms (1.25 s).
const contentionScale = 20

// contentionDocRE matches the documents a scenario's requests name.
var contentionDocRE = regexp.MustCompile(`aruma-ref-txn/[A-Za-z0-9-]+`)

// TestContentionReplay replays every run of f0-contention.json: the events in their order, each
// awaited as long as the recorder awaited it before going on, with the lock wait scaled the same.
// The run's pattern (each request's outcome, in order), every error's text and the documents'
// final writers must be production's. Idleness is not replayed: production's idle transactions
// (s5) commit as busy ones do.
func TestContentionReplay(t *testing.T) {
	h := newHarness(t, apitest.Options{Transactions: documents.TxnConfig{WaitTimeout: 20 * time.Second / contentionScale}})
	tok := h.env.JWT(t)
	var f struct {
		Cases []contentionCase `json:"cases"`
	}
	readCorpusFile(t, "f0-contention.json", &f)
	if len(f.Cases) < 30 {
		t.Fatalf("%d runs", len(f.Cases))
	}
	for _, c := range f.Cases {
		t.Run(c.ID, func(t *testing.T) { replayContention(t, h, tok, c) })
	}
}

func replayContention(t *testing.T, h *harness, tok string, c contentionCase) {
	blocked := 5 * time.Second / contentionScale
	if c.Scenario == "s6a" || c.Scenario == "s6b" {
		blocked = 25 * time.Second / contentionScale
	}
	docs := map[string]bool{}
	for _, e := range c.Events {
		for _, d := range contentionDocRE.FindAllString(e.Request.Path+string(e.Request.Body), -1) {
			docs[d] = true
		}
	}
	for d := range docs {
		if code, _, body := h.do(t, http.MethodPatch, "/v1/"+root+"/"+d, `{"fields":{"writer":{"stringValue":"setup"}}}`, tok); code != 200 {
			t.Fatalf("setup %s: %d %s", d, code, body)
		}
	}
	type result struct {
		code int
		body []byte
		err  error
	}
	var mu sync.Mutex
	ids := map[string]string{} // actor: transaction id
	results := make([]chan result, len(c.Events))
	for i, e := range c.Events {
		mu.Lock()
		path := "/v1/" + strings.ReplaceAll(e.Request.Path, "{project}", apitest.Project)
		body := string(e.Request.Body)
		for actor, id := range ids {
			ph := "<" + actor + " transaction id>"
			path = strings.ReplaceAll(path, ph, url.QueryEscape(id))
			body = strings.ReplaceAll(body, ph, id)
		}
		mu.Unlock()
		if strings.Contains(path, "<") || strings.Contains(body, "<") {
			t.Fatalf("event %d: a placeholder is left: %s %s", e.N, path, body)
		}
		ch := make(chan result, 1)
		results[i] = ch
		go func() {
			code, b, err := send(h.url, e.Request.Method, path, body, tok)
			if e.Op == "beginTransaction" && err == nil {
				mu.Lock()
				ids[e.Actor] = transactionOf(b)
				mu.Unlock()
			}
			ch <- result{code, b, err}
		}()
		// Await the event as the recorder did; one still pending is blocked, and the run goes on.
		select {
		case r := <-ch:
			ch <- r
		case <-time.After(blocked):
		}
	}
	var pattern []string
	for i, e := range c.Events {
		r := <-results[i]
		if r.err != nil {
			t.Fatalf("event %d: %v", e.N, r.err)
		}
		if r.code != e.HTTP {
			t.Errorf("event %d (%s %s): %d %s, want %d", e.N, e.Actor, e.Op, r.code, r.body, e.HTTP)
		}
		outcome := "ok"
		if r.code != 200 {
			var got struct {
				Error struct {
					Message string `json:"message"`
					Status  string `json:"status"`
				} `json:"error"`
			}
			_ = json.Unmarshal(r.body, &got)
			outcome = got.Error.Status
			if e.Error != nil && (got.Error.Message != e.Error.Message || got.Error.Status != e.Error.Status) {
				t.Errorf("event %d (%s %s): %s %q, want %s %q", e.N, e.Actor, e.Op, got.Error.Status, got.Error.Message, e.Error.Status, e.Error.Message)
			}
		}
		if e.Op != "beginTransaction" {
			pattern = append(pattern, e.Actor+" "+e.Op+": "+outcome)
		}
	}
	if got := strings.Join(pattern, "; "); got != c.Pattern {
		t.Errorf("pattern\n got %s\nwant %s", got, c.Pattern)
	}
	for _, key := range slices.Sorted(maps.Keys(c.Final)) {
		want := c.Final[key]
		var name string
		for d := range docs {
			if strings.HasSuffix(d, "-"+key) {
				name = d
			}
		}
		code, _, b := h.do(t, http.MethodGet, "/v1/"+root+"/"+name, "", tok)
		var got struct {
			Fields struct {
				Writer struct {
					StringValue string `json:"stringValue"`
				} `json:"writer"`
				Step *struct {
					StringValue string `json:"stringValue"`
				} `json:"step"`
			} `json:"fields"`
		}
		_ = json.Unmarshal(b, &got)
		step := ""
		if got.Fields.Step != nil {
			step = got.Fields.Step.StringValue
		}
		wantStep := ""
		if want.Step != nil {
			wantStep = *want.Step
		}
		if code != want.HTTP || got.Fields.Writer.StringValue != want.Writer || step != wantStep {
			t.Errorf("final %s: %d writer %q step %q, want %d %q %q", key, code, got.Fields.Writer.StringValue, step, want.HTTP, want.Writer, wantStep)
		}
	}
}

// failingStore fails every read in a transaction of the documents under "fail".
type failingStore struct{ documents.Store }

func (s failingStore) BatchGet(ctx context.Context, db documents.DB, paths []documents.Path, o documents.ReadOptions) ([]documents.Document, time.Time, error) {
	if o.Transaction != nil && len(paths) > 0 && paths[0][0] == "fail" {
		return nil, time.Time{}, documents.ErrWounded
	}
	return s.Store.BatchGet(ctx, db, paths, o)
}

// A read with new_transaction that fails answers the error alone, and its transaction is rolled
// back: the client never learns of it, so it must hold no lock.
func TestNewTransactionReadFails(t *testing.T) {
	h := newHarness(t, apitest.Options{Store: func(s documents.Store) documents.Store { return failingStore{s} }})
	tok := h.env.JWT(t)
	code, _, body := h.do(t, http.MethodPost, "/v1/"+root+":batchGet",
		`{"documents":["`+root+`/fail/d"],"newTransaction":{"readWrite":{}}}`, tok)
	want := `[{"error":{"code":409,"message":"` + documents.ErrWounded.Message + `","status":"ABORTED"}}]`
	if code != http.StatusConflict || string(body) != want {
		t.Errorf("got %d %s\nwant 409 %s", code, body, want)
	}
	// The transaction it began holds no lock: a write of the document does not wait.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := h.env.Store.Commit(ctx, documents.DB{Project: h.env.Number, ProjectID: apitest.Project, ID: "(default)"},
		[]documents.Write{{Kind: documents.WriteDelete, Path: documents.Path{"fail", "d"}}}); err != nil {
		t.Errorf("a write after the failed read: %v", err)
	}
}

// send is a request off the test's goroutine: no t.Fatal.
func send(base, method, path, body, token string) (int, []byte, error) {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// transactionGapsF2 are the cases of f2-transactions.json this server does not answer as
// production, each with its reason. A listed case answered as production fails the test.
var transactionGapsF2 = map[string]string{}

// TestTransactionsReplayF2 replays the REST cases of f2-transactions.json in their order, on the
// file's setup: Begin's options and read times, retry_transaction, reads and commits in a
// transaction, read-only commits, Rollback, unknown, forged and empty ids. A transaction a case
// began stands for the recording's placeholder of it in later requests; the read times the
// recorder replaced are rebuilt from its descriptions. The queries run in their transactions and
// begin them (new_transaction), as production's did.
func TestTransactionsReplayF2(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	var f corpusFile
	readCorpusFile(t, "f2-transactions.json", &f)
	var setupTime string
	for _, c := range f.Setup.Commits {
		for _, w := range c.Writes {
			path := "/v1/" + strings.ReplaceAll(w.Update.Name, "{project}", apitest.Project)
			code, _, body := h.do(t, http.MethodPatch, path, `{"fields":`+string(w.Update.Fields)+`}`, tok)
			if code != 200 {
				t.Fatalf("setup %s: %d %s", path, code, body)
			}
			var d struct {
				UpdateTime string `json:"updateTime"`
			}
			if err := json.Unmarshal(body, &d); err != nil {
				t.Fatal(err)
			}
			setupTime = d.UpdateTime
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	readTimes := map[string]string{
		"begin.read_only_read_time":            setupTime,
		"begin.read_only_read_time_2h":         now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
		"begin.read_only_read_time_8d":         now.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano),
		"begin.read_only_read_time_future":     now.Add(time.Hour).Format(time.RFC3339Nano),
		"begin.read_only_read_time_2h_seconds": now.Add(-2*time.Hour + 30*time.Second).Format(time.RFC3339Nano),
		"begin.read_write_read_time":           setupTime,
	}
	// The ids the recorder wrote as "<transaction>": the empty one, and one not base64.
	plainIDs := map[string]string{"rollback.empty_id": "", "rollback.not_base64": "%%%", "unknown.commit_empty_string": ""}
	ids := map[string]string{} // placeholder: our id, base64
	compared := 0
	for _, c := range f.Cases {
		if c.Transport != "rest" {
			continue
		}
		path, body := c.Request.Path, string(c.Request.Body)
		if rt, ok := readTimes[c.ID]; ok {
			body = strings.ReplaceAll(body, "<readTime: RFC 3339>", rt)
		}
		if id, ok := plainIDs[c.ID]; ok {
			body = strings.ReplaceAll(body, "<transaction>", id)
		}
		if id := ids["<read-write transaction to forge from #14>"]; id != "" {
			b, _ := base64.StdEncoding.DecodeString(id)
			b[len(b)-1] ^= 1
			body = strings.ReplaceAll(body, "<the read-write transaction to forge from, last byte flipped>", base64.StdEncoding.EncodeToString(b))
		}
		if strings.Contains(body, "<string 1336 bytes") {
			body = `{"transaction":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("Z"), 1000)) + `"}`
		}
		for placeholder, id := range ids {
			path = strings.ReplaceAll(path, placeholder, url.QueryEscape(id))
			body = strings.ReplaceAll(body, placeholder, id)
		}
		if strings.Contains(path, "<") || strings.Contains(body, "<") {
			continue // a transaction a query began
		}
		code, _, got := h.do(t, c.Request.Method, "/v1/"+strings.ReplaceAll(path, "{project}", apitest.Project), strings.ReplaceAll(body, "{project}", apitest.Project), tok)
		if placeholder := transactionOf(c.Response.Body); placeholder != "" {
			ids[placeholder] = transactionOf(got)
		}
		compared++
		ok := code == c.Response.HTTP && sameJSON(t, normalise(t, got), normalise(t, c.Response.Body))
		if reason, known := transactionGapsF2[c.ID]; known {
			if ok {
				t.Errorf("%s: answered as production, but listed as a gap (%s)", c.ID, reason)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: %s %s %s\n got %d %s\nwant %d %s", c.ID, c.Request.Method, path, body, code, got, c.Response.HTTP, c.Response.Body)
		}
	}
	t.Logf("compared %d cases", compared)
	if compared < 70 {
		t.Errorf("compared %d cases", compared)
	}
}

// locksCase is a case of f2-locks.json: its events, in order, as the recorder described them.
type locksCase struct {
	ID     string `json:"id"`
	Events []struct {
		What    string `json:"what"`
		HTTP    int    `json:"http"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"events"`
	FinalValue json.RawMessage `json:"finalValue"`
}

// TestLocksReplayF2 replays the cases of f2-locks.json that are not queries (those are F3's):
// a wounded transaction's calls after the wound, and an owner's OPTIMISTIC transaction. The
// recorder kept each event's description, not its request, so the requests are rebuilt from
// them; every answer's status and text must be production's, and the document must end as
// production's did.
func TestLocksReplayF2(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	var f struct {
		Cases []locksCase `json:"cases"`
	}
	readCorpusFile(t, "f2-locks.json", &f)
	doc := func(id string) string { return root + "/aruma-ref-f2-lock/" + id }
	ids := map[string]string{} // T1, T2, OPT: the transactions' ids once begun
	type step func() (int, []byte)
	begin := func(name, body string) step {
		return func() (int, []byte) {
			code, _, b := h.do(t, http.MethodPost, "/v1/"+root+":beginTransaction", body, tok)
			ids[name] = transactionOf(b)
			return code, b
		}
	}
	get := func(id, txn string) step {
		return func() (int, []byte) {
			code, _, b := h.do(t, http.MethodGet, "/v1/"+doc(id)+"?transaction="+url.QueryEscape(ids[txn]), "", tok)
			return code, b
		}
	}
	commit := func(id string, n int, txn string) step {
		return func() (int, []byte) {
			body := `{"writes":[{"update":{"name":"` + doc(id) + `","fields":{"n":{"integerValue":"` + strconv.Itoa(n) + `"}}}}]`
			if txn != "" {
				body += `,"transaction":"` + ids[txn] + `"`
			}
			code, _, b := h.do(t, http.MethodPost, "/v1/"+root+":commit", body+"}", tok)
			return code, b
		}
	}
	rollback := func(txn string) step {
		return func() (int, []byte) {
			code, _, b := h.do(t, http.MethodPost, "/v1/"+root+":rollback", `{"transaction":"`+ids[txn]+`"}`, tok)
			return code, b
		}
	}
	cases := map[string]struct {
		before []step // not events of the recording
		steps  []step // one per event
		doc    string
	}{
		"wounded_transaction": {
			before: []step{begin("T1", `{}`), begin("T2", `{}`)},
			steps: []step{get("wound", "T1"), get("wound", "T2"), commit("wound", 1, "T1"),
				get("wound", "T2"), get("wound", "T2"), commit("wound", 2, "T2"), rollback("T2")},
			doc: "wound",
		},
		"optimistic.options.readWrite.concurrencyMode": {
			steps: []step{begin("OPT", `{"options":{"readWrite":{"concurrencyMode":"OPTIMISTIC"}}}`),
				get("optimistic", "OPT"), commit("optimistic", 2, ""), commit("optimistic", 3, "OPT")},
			doc: "optimistic",
		},
		"optimistic.top-level concurrencyMode": {
			steps: []step{begin("BAD", `{"options":{"readWrite":{}},"concurrencyMode":"OPTIMISTIC"}`)},
		},
	}
	replayed := 0
	for _, c := range f.Cases {
		run, ok := cases[c.ID]
		if !ok {
			continue // a query: F3
		}
		replayed++
		if run.doc != "" {
			if code, _, b := h.do(t, http.MethodPatch, "/v1/"+doc(run.doc), `{"fields":{"n":{"integerValue":"0"}}}`, tok); code != 200 {
				t.Fatalf("setup: %d %s", code, b)
			}
		}
		for _, s := range run.before {
			if code, b := s(); code != 200 {
				t.Fatalf("%s: %d %s", c.ID, code, b)
			}
		}
		if len(run.steps) != len(c.Events) {
			t.Fatalf("%s: %d steps for %d events", c.ID, len(run.steps), len(c.Events))
		}
		for i, e := range c.Events {
			code, b := run.steps[i]()
			var got struct {
				Error struct {
					Message string `json:"message"`
					Status  string `json:"status"`
				} `json:"error"`
			}
			_ = json.Unmarshal(b, &got)
			if code != e.HTTP || got.Error.Message != e.Message || got.Error.Status != e.Status {
				t.Errorf("%s, %s: %d %s %q, want %d %s %q", c.ID, e.What, code, got.Error.Status, got.Error.Message, e.HTTP, e.Status, e.Message)
			}
		}
		if c.FinalValue != nil {
			_, _, b := h.do(t, http.MethodGet, "/v1/"+doc(run.doc), "", tok)
			var d struct {
				Fields struct {
					N json.RawMessage `json:"n"`
				} `json:"fields"`
			}
			_ = json.Unmarshal(b, &d)
			if !sameJSON(t, d.Fields.N, c.FinalValue) {
				t.Errorf("%s: final %s, want %s", c.ID, d.Fields.N, c.FinalValue)
			}
		}
	}
	if replayed != len(cases) {
		t.Errorf("replayed %d of %d cases", replayed, len(cases))
	}
}
