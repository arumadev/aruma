package documents

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Case is one conformance case: steps run in order against a target, each with its checks.
type Case struct {
	// ID names the case, such as "batch-get/new-transaction".
	ID string
	// Must lists the server MUSTs it covers (musts.go); none for a recorded behaviour beyond them.
	Must []int
	// Phase is the phase whose server it judges ("F1").
	Phase string
	// Vars computes values the steps use as {$name}, such as a time two hours ago.
	Vars func() map[string]string
	// Steps run in order.
	Steps []Step
}

// Step is one request and what its answer must satisfy.
type Step struct {
	Name string
	Request
	// Recorded is production's answer to this request, "file.json#case-id" in the oracle corpus.
	// A step without one makes the case Aruma-only.
	Recorded string
	// Setup marks a fixture write. A recording answers it with an empty 200, because the
	// recorder wrote the same documents (its file's setup section) before its cases.
	Setup bool
	// Expect holds the checks of the answer.
	Expect []Check
}

// recordable reports whether every step of the case can be answered from recordings.
func (c Case) recordable() bool {
	return !slices.ContainsFunc(c.Steps, func(s Step) bool { return !s.Setup && s.Recorded == "" })
}

// Context carries a run of one case on one target: its expansions and captured values.
type Context struct {
	Target Target
	vars   map[string]string
	req    Request // the step being checked, expanded
}

// Expand replaces, in s:
//
//	{root}     projects/<project>/databases/(default)/documents
//	{db}       projects/<project>/databases/(default)
//	{project}  the target's project id
//	{run}      the target's run suffix (empty for recordings)
//	{$x}       the value captured or computed as x; {$x:url} query-escapes it
func (c *Context) Expand(s string) string {
	p := c.Target.Project()
	s = strings.NewReplacer(
		"{root}", "projects/"+p+"/databases/(default)/documents",
		"{db}", "projects/"+p+"/databases/(default)",
		"{project}", p,
		"{run}", c.Target.Run(),
	).Replace(s)
	for k, v := range c.vars {
		s = strings.ReplaceAll(s, "{$"+k+":url}", url.QueryEscape(v))
		s = strings.ReplaceAll(s, "{$"+k+"}", v)
	}
	return s
}

// expandAny expands every string of a JSON-like value.
func (c *Context) expandAny(v any) any {
	switch x := v.(type) {
	case string:
		return c.Expand(x)
	case map[string]any:
		o := make(map[string]any, len(x))
		for k, y := range x {
			o[k] = c.expandAny(y)
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, y := range x {
			o[i] = c.expandAny(y)
		}
		return o
	case []string:
		o := make([]any, len(x))
		for i, y := range x {
			o[i] = c.Expand(y)
		}
		return o
	default:
		return v
	}
}

// Run runs every case against the recorded production answers (when all its steps are
// recorded) and against Aruma (when EnvURL is set), as the subtests <case>/production and
// <case>/aruma.
func Run(t *testing.T, cases []Case) {
	t.Helper()
	rec, err := NewRecorded(CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			if c.recordable() {
				t.Run("production", func(t *testing.T) { runOn(t, rec, c) })
			}
			t.Run("aruma", func(t *testing.T) { runOn(t, Aruma(t), c) })
		})
	}
}

func runOn(t *testing.T, target Target, c Case) {
	t.Helper()
	ctx := &Context{Target: target, vars: map[string]string{}}
	if c.Vars != nil {
		for k, v := range c.Vars() {
			ctx.vars[k] = v
		}
	}
	for i, s := range c.Steps {
		name := cmpName(s, i)
		req := s.Request
		req.Path = ctx.Expand(req.Path)
		req.Body = ctx.expandAny(req.Body)
		resp, err := target.Do(t.Context(), s, req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ctx.req = req
		for _, check := range s.Expect {
			if err := check(ctx, resp); err != nil {
				t.Errorf("%s: %s %s: %v (HTTP %d, body %.400s)", name, req.Method, req.Path, err, resp.HTTP, compactJSON(resp.Body))
			}
		}
		if t.Failed() {
			return
		}
	}
}

func cmpName(s Step, i int) string {
	if s.Name != "" {
		return s.Name
	}
	return fmt.Sprintf("step %d", i+1)
}

func compactJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- building requests ----

// Fields encodes a Go map as Firestore REST fields: int as integerValue, float64 as doubleValue,
// string, bool, nil, nested maps, and []any as arrays.
func Fields(m map[string]any) map[string]any {
	o := make(map[string]any, len(m))
	for k, v := range m {
		o[k] = Value(v)
	}
	return o
}

// Value encodes one Go value as a Firestore REST value.
func Value(v any) map[string]any {
	switch x := v.(type) {
	case nil:
		return map[string]any{"nullValue": nil}
	case bool:
		return map[string]any{"booleanValue": x}
	case int:
		return map[string]any{"integerValue": strconv.Itoa(x)}
	case float64:
		return map[string]any{"doubleValue": x}
	case string:
		return map[string]any{"stringValue": x}
	case map[string]any:
		return map[string]any{"mapValue": map[string]any{"fields": Fields(x)}}
	case []any:
		vs := make([]any, len(x))
		for i, y := range x {
			vs[i] = Value(y)
		}
		return map[string]any{"arrayValue": map[string]any{"values": vs}}
	default:
		panic(fmt.Sprintf("Value: %T", v))
	}
}

// Put is a fixture write: UpdateDocument (PATCH) of {root}/rel with data, an upsert that F1's
// server can answer and that the recorder's setup commit is equivalent to.
func Put(rel string, data map[string]any) Step {
	return Step{
		Name:    "put " + rel,
		Request: Request{Method: "PATCH", Path: "{root}/" + rel, Body: map[string]any{"fields": Fields(data)}},
		Setup:   true,
		Expect:  []Check{HTTP(200)},
	}
}

// ---- checks ----

// Check judges one answer.
type Check func(c *Context, r Response) error

// Lookup walks a dotted path through decoded JSON: object keys, and indexes into arrays. The
// empty path is the whole value.
func Lookup(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	for seg := range strings.SplitSeq(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			y, ok := x[seg]
			if !ok {
				return nil, false
			}
			v = y
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// HTTP checks the status code.
func HTTP(code int) Check {
	return func(_ *Context, r Response) error {
		if r.HTTP != code {
			return fmt.Errorf("HTTP %d, want %d", r.HTTP, code)
		}
		return nil
	}
}

// streamingREST are the REST methods of server-streaming RPCs, which answer a JSON array, an
// error included (MUST 20; corpus f1-batch-get.json and f0-find-nearest.json).
var streamingREST = []string{":batchGet", ":runQuery", ":runAggregationQuery", ":executePipeline"}

// errorEnvelope checks MUST 20's error form: the HTTP status, and a body
// {"error": {"code": <the HTTP status>, "message": …, "status": …}} with nothing else in it,
// wrapped in a one-element array for a streaming method.
func errorEnvelope(c *Context, code int, status string, r Response) (string, error) {
	if r.HTTP != code {
		return "", fmt.Errorf("HTTP %d, want %d", r.HTTP, code)
	}
	body := r.Body
	path, _, _ := strings.Cut(c.req.Path, "?")
	if slices.ContainsFunc(streamingREST, func(m string) bool { return strings.HasSuffix(path, m) }) {
		arr, ok := body.([]any)
		if !ok || len(arr) != 1 {
			return "", fmt.Errorf("a streaming method's error body is not [{\"error\": …}]")
		}
		body = arr[0]
	}
	m, ok := body.(map[string]any)
	if !ok || len(m) != 1 {
		return "", fmt.Errorf("the body is not {\"error\": …}")
	}
	e, ok := m["error"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("the body is not {\"error\": …}")
	}
	if c, _ := e["code"].(float64); int(c) != code {
		return "", fmt.Errorf("error.code %v, want %d", e["code"], code)
	}
	if e["status"] != status {
		return "", fmt.Errorf("error.status %v, want %s", e["status"], status)
	}
	msg, ok := e["message"].(string)
	if !ok {
		return "", fmt.Errorf("error.message %v is not a string", e["message"])
	}
	return msg, nil
}

// Error checks an error answer and its exact message, a template for Expand.
func Error(code int, status, message string) Check {
	return ErrorText(code, status, func(c *Context) string { return c.Expand(message) })
}

// ErrorText checks an error answer and the exact message computed for the target.
func ErrorText(code int, status string, message func(c *Context) string) Check {
	return func(c *Context, r Response) error {
		msg, err := errorEnvelope(c, code, status, r)
		if err != nil {
			return err
		}
		if want := message(c); msg != want {
			return fmt.Errorf("error.message %q, want %q", msg, want)
		}
		return nil
	}
}

// ErrorPrefix checks an error answer whose message starts with prefix (for a message whose end
// production varies between runs, or that holds a link).
func ErrorPrefix(code int, status, prefix string) Check {
	return func(c *Context, r Response) error {
		msg, err := errorEnvelope(c, code, status, r)
		if err != nil {
			return err
		}
		if p := c.Expand(prefix); !strings.HasPrefix(msg, p) {
			return fmt.Errorf("error.message %q does not start with %q", msg, p)
		}
		return nil
	}
}

// NotFoundDoc is production's GET of a missing document: 404 NOT_FOUND
// `Document "<name>" not found.`.
func NotFoundDoc(rel string) Check {
	return Error(404, "NOT_FOUND", `Document "{root}/`+rel+`" not found.`)
}

// LacksSlash is production's refusal of a collection path where a document name belongs:
// `<what> "<name>" lacks "/" at index <len(name)>.`.
func LacksSlash(what, name string) Check {
	return ErrorText(400, "INVALID_ARGUMENT", func(c *Context) string {
		n := c.Expand(name)
		return fmt.Sprintf(`%s "%s" lacks "/" at index %d.`, what, n, len(n))
	})
}

// Equal checks the value at path; strings in want are expanded.
func Equal(path string, want any) Check {
	return func(c *Context, r Response) error {
		got, ok := Lookup(r.Body, path)
		if !ok {
			return fmt.Errorf("%q is absent", path)
		}
		w := c.expandAny(want)
		if !jsonEqual(w, got) {
			return fmt.Errorf("%q is %s, want %s", path, compactJSON(got), compactJSON(w))
		}
		return nil
	}
}

func jsonEqual(a, b any) bool {
	var x, y any
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	_ = json.Unmarshal(ab, &x)
	_ = json.Unmarshal(bb, &y)
	return reflect.DeepEqual(x, y)
}

// Header checks one header of the answer. A recorded answer has no headers, so a case using it
// judges a running server only.
func Header(name, want string) Check {
	return func(c *Context, r Response) error {
		if r.Header == nil {
			return fmt.Errorf("the answer carries no headers to check %s", name)
		}
		if got, w := r.Header.Get(name), c.Expand(want); got != w {
			return fmt.Errorf("header %s is %q, want %q", name, got, w)
		}
		return nil
	}
}

// Absent checks that path is not in the answer.
func Absent(path string) Check {
	return func(_ *Context, r Response) error {
		if v, ok := Lookup(r.Body, path); ok {
			return fmt.Errorf("%q is present: %s", path, compactJSON(v))
		}
		return nil
	}
}

// Keys checks that the object at path has exactly these keys.
func Keys(path string, keys ...string) Check {
	return func(_ *Context, r Response) error {
		v, _ := Lookup(r.Body, path)
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%q is not an object", path)
		}
		got := make([]string, 0, len(m))
		for k := range m {
			got = append(got, k)
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(keys))
		if !slices.Equal(got, want) {
			return fmt.Errorf("%q has keys %q, want %q", path, got, want)
		}
		return nil
	}
}

// DocNames checks the names of the documents at path (an array of documents), in order; each
// name is {root}/rel. An absent path counts as no documents, as production omits an empty list.
func DocNames(path string, rels ...string) Check {
	return func(c *Context, r Response) error {
		v, _ := Lookup(r.Body, path)
		arr, _ := v.([]any)
		got := make([]string, 0, len(arr))
		for _, d := range arr {
			n, _ := Lookup(d, "name")
			got = append(got, fmt.Sprint(n))
		}
		want := make([]string, len(rels))
		for i, rel := range rels {
			want[i] = c.Expand("{root}/" + rel)
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf("%q names %q, want %q", path, got, want)
		}
		return nil
	}
}

// Strings checks a list of strings at path, in order; an absent path counts as empty.
func Strings(path string, want ...string) Check {
	return func(c *Context, r Response) error {
		v, _ := Lookup(r.Body, path)
		arr, _ := v.([]any)
		got := make([]string, len(arr))
		for i, s := range arr {
			got[i] = fmt.Sprint(s)
		}
		w := make([]string, len(want))
		for i, s := range want {
			w[i] = c.Expand(s)
		}
		if !slices.Equal(got, w) {
			return fmt.Errorf("%q is %q, want %q", path, got, w)
		}
		return nil
	}
}

// Capture stores the string at path as {$name} for later steps.
func Capture(name, path string) Check {
	return func(c *Context, r Response) error {
		v, ok := Lookup(r.Body, path)
		s, isString := v.(string)
		if !ok || !isString || s == "" {
			return fmt.Errorf("%q is not a non-empty string", path)
		}
		c.vars[name] = s
		return nil
	}
}

var (
	rfc3339Micros   = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{3}|\.\d{6})?Z$`)
	timePlaceholder = regexp.MustCompile(`^<\w+: RFC 3339>$`)
)

// Timestamps checks that each path holds a time at microsecond precision in UTC: production's
// update, create, read and commit times are microseconds (corpus f1-write-results.json,
// precision: every REST time had six fraction digits, every gRPC nanos a multiple of 1000).
// Proto3 JSON may print 0, 3 or 6 digits; nine is never right. A recording holds a placeholder.
func Timestamps(paths ...string) Check {
	return func(c *Context, r Response) error {
		for _, p := range paths {
			v, _ := Lookup(r.Body, p)
			s, _ := v.(string)
			if c.Target.Recorded() && timePlaceholder.MatchString(s) {
				continue
			}
			if !rfc3339Micros.MatchString(s) {
				return fmt.Errorf("%q is %q, not an RFC 3339 UTC time at microsecond precision", p, s)
			}
		}
		return nil
	}
}

// CreateTimeIsUpdateTime checks that a document just created has createTime equal to
// updateTime. A recording holds placeholders, so it uses the relation the recorder measured.
func CreateTimeIsUpdateTime() Check {
	return func(c *Context, r Response) error {
		if c.Target.Recorded() {
			if v, _ := Lookup(r.Case, "relations.createTimeEqualsUpdateTime"); v != true {
				return fmt.Errorf("the recording does not show createTime == updateTime")
			}
			return nil
		}
		ct, _ := Lookup(r.Body, "createTime")
		ut, _ := Lookup(r.Body, "updateTime")
		if ct == nil || ct != ut {
			return fmt.Errorf("createTime %v, updateTime %v", ct, ut)
		}
		return nil
	}
}

var autoID = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// AutoID checks that the name at path is {root}/<collection>/<an automatic id>: 20 characters of
// [A-Za-z0-9] (corpus f1-create.json, autoIds: 25 ids, all 20 long, all 62 characters seen).
func AutoID(path, collection string) Check {
	return func(c *Context, r Response) error {
		v, _ := Lookup(r.Body, path)
		s, _ := v.(string)
		prefix := c.Expand("{root}/" + collection + "/")
		id, ok := strings.CutPrefix(s, prefix)
		if !ok {
			return fmt.Errorf("%q is %q, not under %s", path, s, prefix)
		}
		if c.Target.Recorded() && strings.HasPrefix(id, "<auto id") {
			return nil
		}
		if !autoID.MatchString(id) {
			return fmt.Errorf("automatic id %q is not 20 characters of [A-Za-z0-9]", id)
		}
		return nil
	}
}

// BatchGetAnswers checks a BatchGetDocuments answer (MUST 16): one element per distinct name
// requested, found ones as {found: document, readTime} and missing ones as {missing: name,
// readTime}, ignoring order and an element that carries only a new transaction. Production
// answers a name requested twice once (corpus f1-batch-get.json, rest.duplicates).
func BatchGetAnswers(found, missing []string) Check {
	return func(c *Context, r Response) error {
		arr, ok := r.Body.([]any)
		if !ok {
			return fmt.Errorf("the body is not an array")
		}
		var gotFound, gotMissing []string
		for i, e := range arr {
			m, _ := e.(map[string]any)
			if len(m) == 1 && m["transaction"] != nil {
				continue
			}
			if _, ok := m["readTime"]; !ok {
				return fmt.Errorf("element %d has no readTime", i)
			}
			switch {
			case m["found"] != nil:
				n, _ := Lookup(m, "found.name")
				gotFound = append(gotFound, fmt.Sprint(n))
			case m["missing"] != nil:
				gotMissing = append(gotMissing, fmt.Sprint(m["missing"]))
			default:
				return fmt.Errorf("element %d is neither found nor missing", i)
			}
		}
		expand := func(rels []string) []string {
			o := make([]string, len(rels))
			for i, rel := range rels {
				o[i] = c.Expand("{root}/" + rel)
			}
			slices.Sort(o)
			return o
		}
		slices.Sort(gotFound)
		slices.Sort(gotMissing)
		if w := expand(found); !slices.Equal(gotFound, w) {
			return fmt.Errorf("found %q, want %q", gotFound, w)
		}
		if w := expand(missing); !slices.Equal(gotMissing, w) {
			return fmt.Errorf("missing %q, want %q", gotMissing, w)
		}
		return nil
	}
}

// TransactionFirst checks MUST 16's new_transaction answer as production sends it: the first
// element holds the transaction id alone, and no other element holds one (corpus
// f1-batch-get.json, rest.new_transaction.*).
func TransactionFirst() Check {
	return func(_ *Context, r Response) error {
		arr, _ := r.Body.([]any)
		if len(arr) == 0 {
			return fmt.Errorf("no elements")
		}
		first, _ := arr[0].(map[string]any)
		if t, _ := first["transaction"].(string); len(first) != 1 || t == "" {
			return fmt.Errorf("the first element is %s, not {transaction}", compactJSON(first))
		}
		for i, e := range arr[1:] {
			if m, _ := e.(map[string]any); m["transaction"] != nil {
				return fmt.Errorf("element %d also holds a transaction", i+1)
			}
		}
		return nil
	}
}
