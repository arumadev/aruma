package rest_test

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
