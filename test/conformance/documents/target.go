// Package documents is the protocol-level conformance suite of Aruma's Document Database
// (ADR-028 §7.2, the contract tests). Each server MUST of the client research
// (.cache/briefs/research/firestore-clients.md, "server MUST", catalogued in musts.go) and each
// recorded behaviour of a phase is a Case: REST steps, each with checks, run against a Target.
//
// There are two targets:
//
//   - Recorded production answers, the corpus of test/oracle/documents. Every case whose steps
//     name a recording runs against it in plain go test, offline. This proves that each
//     expectation written here is what production does.
//   - An Aruma endpoint, when ARUMA_DOCUMENTS_URL names one. Without the variable the Aruma half
//     of every case skips; with it, a server that cannot be reached, or that does not answer the
//     Firestore REST protocol, fails the run. It never skips.
//
// The package is standard library only and speaks REST (the transport the Admin SDKs' preferRest
// and the JS lite SDK use), and, for MUST 21, one WebChannel session (webchannel.go). test/differential/documents reuses its Aruma target.
package documents

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The environment that turns on the Aruma half of the suite (and of test/differential/documents).
const (
	// EnvURL is the base URL of an Aruma server with the Document Database enabled, such as
	// http://127.0.0.1:8080; requests go to EnvURL + "/v1/projects/…".
	EnvURL = "ARUMA_DOCUMENTS_URL"
	// EnvProject is the project id the requests name. Required with EnvURL.
	EnvProject = "ARUMA_DOCUMENTS_PROJECT"
	// EnvToken is sent as "Authorization: Bearer <token>": an admin credential of that project
	// (a service-account access token, or documents.dev_owner_token in dev mode). Optional.
	EnvToken = "ARUMA_DOCUMENTS_TOKEN"
)

// CorpusDir is the oracle's corpus, relative to this package (and to test/differential/documents).
const CorpusDir = "../../oracle/documents/corpus"

// Request is one REST call. Path is relative to /v1/. Body is JSON-encodable, or nil.
type Request struct {
	Method      string
	Path        string
	Body        any
	ContentType string            // "application/json" when empty
	NoAuth      bool              // send no Authorization header
	Header      map[string]string // further request headers, such as a browser's Origin
}

// Response is what a target answered: the HTTP status and the decoded JSON body (a string
// holding the raw text when it is not JSON). Case is the recorded case behind a recorded answer.
type Response struct {
	HTTP   int
	Body   any
	Case   map[string]any
	Header http.Header // the answer's headers; nil for a recorded answer
}

// Target answers the steps of a case.
type Target interface {
	// Name is "production" or "aruma".
	Name() string
	// Project is the project id that resource names and messages carry.
	Project() string
	// Run is appended to every collection id written {run}: a suffix unique to this process for
	// a live server, so that runs never see each other's documents; empty for recordings.
	Run() string
	// Recorded reports whether answers are recordings, whose times, tokens and transaction ids
	// are placeholders.
	Recorded() bool
	// Do answers one step whose request has been expanded.
	Do(ctx context.Context, step Step, req Request) (Response, error)
}

// ---- Aruma over HTTP ----

// HTTPTarget sends requests to a live server.
type HTTPTarget struct {
	BaseURL   string
	ProjectID string
	Token     string
	RunSuffix string
	Client    *http.Client
}

func (h *HTTPTarget) Name() string    { return "aruma" }
func (h *HTTPTarget) Project() string { return h.ProjectID }
func (h *HTTPTarget) Run() string     { return h.RunSuffix }
func (h *HTTPTarget) Recorded() bool  { return false }

func (h *HTTPTarget) Do(ctx context.Context, _ Step, req Request) (Response, error) {
	return h.Send(ctx, req)
}

// Send sends one request and decodes the answer.
func (h *HTTPTarget) Send(ctx context.Context, req Request) (Response, error) {
	if req.Method == MethodWebChannel {
		return h.webChannel(ctx, req)
	}
	var body io.Reader
	if req.Body != nil {
		b, err := json.Marshal(req.Body)
		if err != nil {
			return Response{}, err
		}
		body = bytes.NewReader(b)
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, strings.TrimSuffix(h.BaseURL, "/")+"/v1/"+req.Path, body)
	if err != nil {
		return Response{}, err
	}
	hr.Header.Set("Content-Type", cmp.Or(req.ContentType, "application/json"))
	if h.Token != "" && !req.NoAuth {
		hr.Header.Set("Authorization", "Bearer "+h.Token)
	}
	for k, v := range req.Header {
		hr.Header.Set(k, v)
	}
	res, err := h.Client.Do(hr)
	if err != nil {
		return Response{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return Response{}, err
	}
	out := Response{HTTP: res.StatusCode, Body: string(raw), Header: res.Header}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		out.Body = v
	}
	return out, nil
}

// ArumaFromEnv builds the Aruma target from the environment and checks that it answers the
// Firestore REST protocol: a GET of a document that does not exist must come back as JSON
// holding a document or an error. ok is false when EnvURL is not set.
func ArumaFromEnv(ctx context.Context) (t *HTTPTarget, ok bool, err error) {
	base := os.Getenv(EnvURL)
	if base == "" {
		return nil, false, nil
	}
	project := os.Getenv(EnvProject)
	if project == "" {
		return nil, true, fmt.Errorf("%s is set but %s is not", EnvURL, EnvProject)
	}
	t = &HTTPTarget{
		BaseURL:   base,
		ProjectID: project,
		Token:     os.Getenv(EnvToken),
		RunSuffix: fmt.Sprintf("-%x", time.Now().UnixNano()&0xffffffff),
		Client:    &http.Client{Timeout: 60 * time.Second},
	}
	return t, true, t.Probe(ctx)
}

// Probe checks that the server answers the Firestore REST protocol.
func (h *HTTPTarget) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeoutCause(ctx, 15*time.Second, errors.New("the probe of the Aruma server timed out"))
	defer cancel()
	path := fmt.Sprintf("projects/%s/databases/(default)/documents/aruma-ref-probe%s/x", h.ProjectID, h.RunSuffix)
	r, err := h.Send(ctx, Request{Method: http.MethodGet, Path: path})
	if err != nil {
		return fmt.Errorf("%s=%s does not answer: %w", EnvURL, h.BaseURL, err)
	}
	if m, ok := r.Body.(map[string]any); !ok || (m["error"] == nil && m["name"] == nil) {
		return fmt.Errorf("%s=%s answers GET /v1/%s with HTTP %d and %.200v, not the Firestore REST protocol", EnvURL, h.BaseURL, path, r.HTTP, r.Body)
	}
	return nil
}

var arumaOnce = sync.OnceValues(func() (*HTTPTarget, error) {
	t, _, err := ArumaFromEnv(context.Background())
	return t, err
})

// Aruma returns the live target. Without EnvURL it skips the calling test; with EnvURL, a
// server that cannot be reached fails it.
func Aruma(t testingT) *HTTPTarget {
	t.Helper()
	return aruma(t, arumaOnce)
}

func aruma(t testingT, connect func() (*HTTPTarget, error)) *HTTPTarget {
	t.Helper()
	if os.Getenv(EnvURL) == "" {
		t.Skipf("%s is not set: the Aruma half runs when it names a server", EnvURL)
		return nil
	}
	h, err := connect()
	if err != nil {
		t.Fatalf("%v", err)
		return nil
	}
	return h
}

// testingT is the part of *testing.T that Aruma uses.
type testingT interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// ---- recorded production answers ----

// RecordedTarget answers from the oracle corpus. A step names its recording as
// "file.json#case-id"; the request the step sends must be the recorded request, except where the
// recording holds a placeholder such as "<transaction>" or "<nextPageToken of page 1>".
type RecordedTarget struct {
	dir     string
	project string
	mu      sync.Mutex
	files   map[string]map[string]map[string]any
}

// NewRecorded reads recordings from dir (CorpusDir). The project is the one the corpus was
// recorded in, read from the first file's header.
func NewRecorded(dir string) (*RecordedTarget, error) {
	r := &RecordedTarget{dir: dir, files: map[string]map[string]map[string]any{}}
	var h struct {
		Header struct {
			Target string `json:"target"`
		} `json:"header"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "f1-get.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	r.project, _, _ = strings.Cut(h.Header.Target, " ")
	if r.project == "" {
		return nil, errors.New("f1-get.json: no project in header.target")
	}
	return r, nil
}

func (r *RecordedTarget) Name() string    { return "production" }
func (r *RecordedTarget) Project() string { return r.project }
func (r *RecordedTarget) Run() string     { return "" }
func (r *RecordedTarget) Recorded() bool  { return true }

// Case returns the recorded case "file.json#id".
func (r *RecordedTarget) Case(ref string) (map[string]any, error) {
	file, id, ok := strings.Cut(ref, "#")
	if !ok {
		return nil, fmt.Errorf("recording %q is not file.json#case-id", ref)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cases, ok := r.files[file]
	if !ok {
		var f struct {
			Cases []map[string]any `json:"cases"`
		}
		b, err := os.ReadFile(filepath.Join(r.dir, file))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		cases = map[string]map[string]any{}
		for _, c := range f.Cases {
			if s, ok := c["id"].(string); ok {
				cases[s] = c
			}
		}
		r.files[file] = cases
	}
	c, ok := cases[id]
	if !ok {
		return nil, fmt.Errorf("%s: no case %q", file, id)
	}
	return c, nil
}

// Do returns the recorded answer, after checking that the step sends the recorded request.
// A setup step (a fixture write) is answered with an empty 200: the recorder wrote the same
// documents before its cases.
func (r *RecordedTarget) Do(_ context.Context, step Step, req Request) (Response, error) {
	if step.Setup {
		return Response{HTTP: http.StatusOK, Body: map[string]any{}}, nil
	}
	if step.Recorded == "" {
		return Response{}, errors.New("the step names no recording")
	}
	c, err := r.Case(step.Recorded)
	if err != nil {
		return Response{}, err
	}
	if c["transport"] != "rest" {
		return Response{}, fmt.Errorf("%s is a %v recording; the suite speaks REST", step.Recorded, c["transport"])
	}
	rq, _ := c["request"].(map[string]any)
	norm := func(s string) string {
		return strings.ReplaceAll(s, "projects/"+r.project+"/", "projects/{project}/")
	}
	if rq["method"] != req.Method {
		return Response{}, fmt.Errorf("%s was recorded as %v, the step sends %s", step.Recorded, rq["method"], req.Method)
	}
	if want, _ := rq["path"].(string); !placeholderPattern(want).MatchString(norm(req.Path)) {
		return Response{}, fmt.Errorf("%s was recorded with path %q, the step sends %q", step.Recorded, want, norm(req.Path))
	}
	if want, has := rq["body"]; has || req.Body != nil {
		var got any
		b, _ := json.Marshal(req.Body)
		_ = json.Unmarshal([]byte(norm(string(b))), &got)
		if !MatchJSON(want, got) {
			wb, _ := json.Marshal(want)
			return Response{}, fmt.Errorf("%s was recorded with body %s, the step sends %s", step.Recorded, wb, b)
		}
	}
	resp, _ := c["response"].(map[string]any)
	code, _ := resp["http"].(float64)
	return Response{HTTP: int(code), Body: resp["body"], Case: c}, nil
}

var placeholder = regexp.MustCompile(`<[^<>]*>`)

// placeholderPattern matches a recorded string exactly, except that each "<…>" placeholder
// matches any run of characters up to the next "&".
func placeholderPattern(recorded string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, m := range placeholder.FindAllStringIndex(recorded, -1) {
		b.WriteString(regexp.QuoteMeta(recorded[last:m[0]]))
		b.WriteString("[^&]*")
		last = m[1]
	}
	b.WriteString(regexp.QuoteMeta(recorded[last:]))
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// MatchJSON reports whether got equals a recorded JSON value, where a recorded string that is
// one "<…>" placeholder stands for any value.
func MatchJSON(recorded, got any) bool {
	switch w := recorded.(type) {
	case string:
		if strings.HasPrefix(w, "<") && strings.HasSuffix(w, ">") && placeholder.FindString(w) == w {
			return got != nil
		}
		return placeholderPattern(w).MatchString(fmt.Sprint(got)) && isString(got)
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, v := range w {
			gv, ok := g[k]
			if !ok || !MatchJSON(v, gv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !MatchJSON(w[i], g[i]) {
				return false
			}
		}
		return true
	default:
		return recorded == got
	}
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}
