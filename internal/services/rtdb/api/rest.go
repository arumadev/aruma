package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/services/rtdb/core"
	"github.com/arumadev/aruma/internal/services/rtdb/rules"
)

// The REST API on /<path>.json (ADR-020 §3), as the Go and Python Admin SDKs and firebase-tools
// use it (H-rules-rest-lp.md §Q5): GET, PUT, POST, PATCH and DELETE, with print, shallow,
// format=export, the query parameters (rest_query.go), ETags with if-match and if-none-match,
// and the credentials of rest_auth.go. Each request opens a session of its own and runs
// through the database's writer like an SDK's command, so listeners see REST writes as they
// commit, and a REST read sees every write acked before it.
//
// Answers follow the emulator 4.11.2, recorded in testdata/emulator/rest.golden.json and
// replayed against Aruma by test/acceptance (TestRTDBRESTMatchesEmulator): bodies are JSON
// with application/json; charset=utf-8 and Cache-Control: no-cache, errors are
// {"error" : "..."} pretty-printed, as the emulator and production print them.
//
// Not served: streaming (Accept: text/event-stream), which ADR-020 defers, so the Python Admin
// SDK's listen() fails at once with the 400 below; JSONP (callback=), which no SDK sends and
// is ignored; and download=.

// restError is a REST answer that is an error: its status and the text of {"error": ...}.
type restError struct {
	status int
	msg    string
}

func restErr(status int, msg string) *restError { return &restError{status, msg} }

func (e *restError) Error() string { return e.msg }

var (
	errUnsupportedMethod = restErr(http.StatusMethodNotAllowed, "Unsupported request type")
	errStreaming         = restErr(http.StatusBadRequest,
		"Streaming (Accept: text/event-stream) is not supported by this server; read with GET or listen with an SDK.")
	errTimedOut = restErr(http.StatusBadRequest, "The request timed out.")
)

// restMethods are the methods REST serves, as OPTIONS lists them (emulator 4.11.2).
const restMethods = "OPTIONS,GET,POST,PUT,DELETE,PATCH"

// nullETag is the ETag of a node that does not exist (emulator 4.11.2): the node's hash is
// empty, and an empty header would read as no ETag at all.
const nullETag = "null_etag"

// maxTimeout is the longest timeout= Firebase accepts.
const maxTimeout = 15 * time.Minute

// restRequest is a REST request, read and checked before it touches the database.
type restRequest struct {
	method  string
	path    string // the data path, without .json
	export  bool   // format=export
	pretty  bool   // print=pretty
	silent  bool   // print=silent
	shallow bool
	// shallowLimit is a shallow read's limitToFirst (> 0) or limitToLast (< 0), which
	// firebase-tools sends without orderBy to delete a large node a page at a time
	// (database/listRemote.ts); the keys then go in lexicographic order.
	shallowLimit int64
	query        *core.QuerySpec
	etag         bool    // X-Firebase-ETag: true
	ifMatch      *string // if-match, for a conditional PUT or DELETE
	ifNone       *string // if-none-match, for a GET
	timeout      time.Duration
}

// serveREST answers a request to /<path>.json.
func (a *API) serveREST(w http.ResponseWriter, r *http.Request, t core.Target) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	req, err := readREST(r)
	if err != nil {
		writeRESTError(w, err)
		return
	}
	if req.method == http.MethodOptions {
		h.Set("Allow", restMethods)
		h.Set("Access-Control-Allow-Methods", "GET, PUT, POST, PATCH, DELETE, OPTIONS")
		if want := r.Header.Get("Access-Control-Request-Headers"); want != "" {
			h.Set("Access-Control-Allow-Headers", want)
		}
		h.Set("Access-Control-Max-Age", "3600")
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx := r.Context()
	if req.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.timeout)
		defer cancel()
	}
	s, err := a.openREST(ctx, r, t)
	if err != nil {
		writeRESTError(w, err)
		return
	}
	defer s.Close()
	switch req.method {
	case http.MethodGet:
		a.restGet(ctx, w, s, t, req)
	case http.MethodPut, http.MethodDelete:
		a.restSet(ctx, w, r, s, req)
	case http.MethodPost:
		a.restPush(ctx, w, r, s, req)
	case http.MethodPatch:
		a.restUpdate(ctx, w, r, s, req)
	}
}

// readREST reads a request's method, path and parameters, and refuses the combinations the
// emulator refuses, with its texts.
func readREST(r *http.Request) (*restRequest, error) {
	q := r.URL.Query()
	req := &restRequest{method: r.Method}
	// X-HTTP-Method-Override lets a client that can only send GET and POST send the others.
	if o := r.Header.Get("X-HTTP-Method-Override"); o != "" {
		req.method = strings.ToUpper(o)
	} else if o := q.Get("x-http-method-override"); o != "" {
		req.method = strings.ToUpper(o)
	}
	switch req.method {
	case http.MethodGet, http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		return nil, errUnsupportedMethod
	}
	if req.method == http.MethodOptions {
		return req, nil
	}
	req.path = strings.TrimSuffix(r.URL.Path, ".json")

	if f := q.Get("format"); q.Has("format") && f != "export" {
		return nil, restErr(400, "Export format must be export if present")
	}
	req.export = q.Has("format")
	switch q.Get("print") {
	case "pretty":
		req.pretty = true
	case "silent":
		req.silent = true
	}
	req.shallow = q.Get("shallow") == "true"
	if q.Has("timeout") {
		d, err := parseTimeout(q.Get("timeout"))
		if err != nil {
			return nil, err
		}
		req.timeout = d
	}
	if q.Has("writeSizeLimit") {
		switch q.Get("writeSizeLimit") {
		case "tiny", "small", "medium", "large", "unlimited":
			// Firebase aborts writes it estimates would take longer than the limit's target;
			// Aruma's writes are bounded by rest_write_bytes instead, so every limit is
			// accepted and none aborts.
		default:
			// The set in production's order (production/oversize.json).
			return nil, restErr(400, "writeSizeLimit should be one of {tiny, large, small, medium, unlimited}")
		}
	}
	if req.shallow && !q.Has("orderBy") {
		for _, name := range []string{"limitToFirst", "limitToLast"} {
			if !q.Has(name) {
				continue
			}
			n, err := strconv.ParseInt(q.Get(name), 10, 32)
			switch {
			case err != nil:
				return nil, restErr(400, name+" must be an integer")
			case n <= 0:
				return nil, restErr(400, name+" must be positive")
			case req.shallowLimit != 0:
				return nil, restErr(400, "Only one of limitToFirst and limitToLast may be specified")
			}
			if name == "limitToLast" {
				n = -n
			}
			req.shallowLimit = n
			q.Del(name)
		}
	}
	var err error
	if req.query, err = parseQuery(q); err != nil {
		return nil, err
	}
	if req.query != nil && req.method != http.MethodGet {
		if req.method == http.MethodDelete {
			return nil, restErr(400, "Unsupported operation")
		}
		return nil, restErr(400, "Querying related parameters not supported on this request type")
	}
	if req.shallow && req.query != nil {
		return nil, restErr(400, "orderBy not supported for with shallow GET, keys are returned in lexicographical order")
	}

	req.etag = strings.EqualFold(r.Header.Get("X-Firebase-ETag"), "true")
	if req.etag && req.method == http.MethodPatch {
		return nil, restErr(400, "The 'X-Firebase-ETag' header is not supported with PATCH requests")
	}
	if req.etag && req.query != nil {
		return nil, restErr(400, "Mixing querying parameters and ETag requests is not supported")
	}
	if req.etag && req.shallow {
		return nil, restErr(400, "Mixing 'shallow' and etag requests is not supported")
	}
	if v, ok := header(r, "If-Match"); ok {
		if req.method != http.MethodPut && req.method != http.MethodDelete {
			return nil, restErr(400, "The 'if-match' header is not supported with GET, PATCH or POST requests")
		}
		req.ifMatch = &v
	}
	if v, ok := header(r, "If-None-Match"); ok {
		// On a PUT it makes the write conditional the other way round: it applies unless
		// the node's ETag is the one given (emulator 4.11.2).
		if req.method != http.MethodGet && req.method != http.MethodPut {
			return nil, restErr(400, "The 'if-none-match' header is not supported with PUT, POST, PATCH or DELETE requests")
		}
		req.ifNone = &v
	}
	if (req.ifMatch != nil || req.ifNone != nil) && (req.shallow || req.query != nil || req.silent) {
		return nil, restErr(400, "Mixing 'shallow', querying parameters or 'print=silent' and if-match or if-none-match requests is not supported")
	}
	if req.method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return nil, errStreaming
	}
	return req, nil
}

// header returns a request header and whether it is present at all.
func header(r *http.Request, name string) (string, bool) {
	v, ok := r.Header[http.CanonicalHeaderKey(name)]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

var timeoutRE = regexp.MustCompile(`^([0-9]+)(ms|s|seconds|min|minutes)$`)

// parseTimeout reads timeout=: a length and a unit, as Firebase documents it (3s, 2seconds,
// 3min), at most 15 minutes.
func parseTimeout(v string) (time.Duration, error) {
	m := timeoutRE.FindStringSubmatch(v)
	if m == nil {
		return 0, restErr(400, "timeout must contain length and unit (s/seconds/min/minutes), such as 1s, 2seconds, or 3min")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > int64(maxTimeout/time.Millisecond) {
		return 0, restErr(400, "timeout must not be longer than 15 minutes")
	}
	unit := map[string]time.Duration{"ms": time.Millisecond, "s": time.Second, "seconds": time.Second,
		"min": time.Minute, "minutes": time.Minute}[m[2]]
	d := time.Duration(n) * unit
	if d > maxTimeout {
		return 0, restErr(400, "timeout must not be longer than 15 minutes")
	}
	return d, nil
}

// await runs one session command and waits for it until ctx ends. The command runs on even
// if ctx ends first; its result is then dropped, and the session's Close waits for it.
func await[T any](ctx context.Context, run func(done func(T, error))) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	run(func(v T, err error) { ch <- result{v, err} })
	select {
	case res := <-ch:
		return res.v, res.err
	case <-ctx.Done():
		var zero T
		return zero, errTimedOut
	}
}

// restGet answers a GET: the node, the part a query selects, or with shallow its keys.
func (a *API) restGet(ctx context.Context, w http.ResponseWriter, s *core.Session, t core.Target, req *restRequest) {
	path := req.path
	priority := false
	if p, ok := strings.CutSuffix(strings.TrimSuffix(path, "/"), "/"+core.PriorityKey); ok {
		// /<path>/.priority.json reads the priority of <path>.
		path, priority = p, true
	}
	n, err := await(ctx, func(done func(*core.Node, error)) { s.GetREST(path, req.query, req.shallow, done) })
	if err != nil {
		writeRESTError(w, err)
		return
	}
	var body []byte
	switch {
	case priority:
		n = n.Priority()
		body = n.JSON()
	case req.shallow:
		if body, err = a.shallow(ctx, t, path, req.shallowLimit); err != nil {
			writeRESTError(w, err)
			return
		}
	case n == nil && req.query != nil && req.query.Limit == 0:
		// A query that selects nothing answers an empty object, or null when it has a limit
		// (emulator 4.11.2).
		body = []byte("{}")
	case req.export:
		body = n.ExportJSON()
	default:
		body = n.JSON()
	}
	if req.etag || req.ifNone != nil {
		etag := etagOf(n)
		w.Header().Set("ETag", etag)
		w.Header().Set("Access-Control-Expose-Headers", "ETag")
		if req.ifNone != nil && *req.ifNone == etag {
			if !req.etag {
				w.Header().Del("ETag")
			}
			setRESTHeaders(w)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	writeRESTBody(w, http.StatusOK, body, req)
}

// shallow renders the node at path with every child as true, a leaf as itself (emulator
// 4.11.2; the REST reference), and ".priority": true first when the object has a priority,
// as production and the emulator both answer (production/rest.json); a leaf's priority does
// not show. Under limitToFirst or limitToLast the emulator counts .priority as the first key.
// It reads the keys from the stored rows a page at a time, without loading what lies below
// them; GetREST checked the rules first.
func (a *API) shallow(ctx context.Context, t core.Target, path string, limit int64) ([]byte, error) {
	var keys []string
	after := ""
	priority := false
	for {
		page, err := a.svc.Shallow(ctx, t, path, after, core.MaxShallowPage)
		if err != nil {
			return nil, err
		}
		if page.Value != nil {
			return page.Value.JSON(), nil
		}
		priority = priority || page.Priority != nil
		for _, c := range page.Children {
			keys = append(keys, c.Key)
		}
		if page.Next == "" || limit > 0 && int64(len(keys)) >= limit {
			break
		}
		after = page.Next
	}
	if len(keys) == 0 {
		return []byte("null"), nil
	}
	if limit != 0 {
		// Pages come in byte order, but a node held in one page comes in key order.
		slices.Sort(keys)
	}
	if priority {
		keys = append([]string{core.PriorityKey}, keys...)
	}
	if limit != 0 {
		if limit > 0 {
			keys = keys[:min(int(limit), len(keys))]
		} else {
			keys = keys[max(len(keys)+int(limit), 0):]
		}
	}
	b := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		name, _ := json.Marshal(k)
		b = append(b, name...)
		b = append(b, ":true"...)
	}
	return append(b, '}'), nil
}

// restSet answers a PUT, and a DELETE as a PUT of null; with if-match only while the node's
// ETag is still the one given, else 412 with the current value and its ETag.
func (a *API) restSet(ctx context.Context, w http.ResponseWriter, r *http.Request, s *core.Session, req *restRequest) {
	if _, err := core.ParseWritePath(req.path); err != nil {
		writeRESTError(w, err)
		return
	}
	data := json.RawMessage("null")
	if req.method == http.MethodPut {
		var err error
		if data, err = readBody(r); err != nil {
			writeRESTError(w, err)
			return
		}
	}
	var hash *string
	if req.ifMatch != nil && *req.ifMatch != "*" {
		h := *req.ifMatch
		if h == nullETag {
			h = ""
		}
		hash = &h
	}
	if req.ifNone != nil {
		// Unless the node's ETag is the one given: read it, then write on condition that it
		// has not changed since, so the check and the write are one.
		cur, err := await(ctx, func(done func(*core.Node, error)) { s.GetREST(req.path, nil, false, done) })
		if err != nil {
			writeRESTError(w, err)
			return
		}
		if etagOf(cur) == *req.ifNone {
			// 412 with the value, as for if-match, but without its ETag, which the client
			// just sent (emulator 4.11.2).
			body := cur.JSON()
			if req.export {
				body = cur.ExportJSON()
			}
			writeRESTBody(w, http.StatusPreconditionFailed, body, &restRequest{pretty: req.pretty})
			return
		}
		h := core.Hash(cur)
		hash = &h
	}
	n, err := await(ctx, func(done func(*core.Node, error)) { s.SetREST(req.path, data, hash, done) })
	if errors.Is(err, core.ErrDataStale) {
		a.restStale(ctx, w, s, req)
		return
	}
	if err != nil {
		writeRESTError(w, dataError(err))
		return
	}
	if req.etag || req.ifNone != nil {
		w.Header().Set("ETag", etagOf(n))
		w.Header().Set("Access-Control-Expose-Headers", "ETag")
	}
	writeRESTBody(w, http.StatusOK, n.EchoJSON(), req)
}

// dataError is an error found in a write's data as REST names it: the path errors of the
// data's keys are the emulator's generic invalid data, and a key error names its place.
func dataError(err error) error {
	var ke *core.KeyError
	switch {
	case errors.As(err, &ke):
		return restErr(400, fmt.Sprintf("Invalid data; couldn't parse key beginning at %d:%d. Key value can't be empty or contain $ # [ ] / or .", ke.Line, ke.Column))
	case errors.Is(err, core.ErrPathTooDeep), errors.Is(err, core.ErrPathTooLong),
		errors.Is(err, core.ErrInvalidPriority), errors.Is(err, core.ErrInvalidServerValue):
		return core.ErrInvalidData
	}
	return err
}

// restStale answers a conditional write whose ETag no longer matches: 412 with the value as it
// is and its ETag, which the Admin SDKs' transactions retry with (ref.go:247-281, db.py:376-420).
// The value is read anew, with the rules of a read: a client that may not read the node gets
// 401, not its data.
func (a *API) restStale(ctx context.Context, w http.ResponseWriter, s *core.Session, req *restRequest) {
	path := req.path
	if p, ok := strings.CutSuffix(path, "/"+core.PriorityKey); ok {
		path = p
	}
	n, err := await(ctx, func(done func(*core.Node, error)) { s.GetREST(path, nil, false, done) })
	if err != nil {
		writeRESTError(w, err)
		return
	}
	w.Header().Set("ETag", etagOf(n))
	w.Header().Set("Access-Control-Expose-Headers", "ETag")
	body := n.JSON()
	if req.export {
		body = n.ExportJSON()
	}
	writeRESTBody(w, http.StatusPreconditionFailed, body, &restRequest{pretty: req.pretty})
}

// restPush answers a POST: the data as a new child under a push id made here, as the SDKs make
// them, and {"name": <id>}.
func (a *API) restPush(ctx context.Context, w http.ResponseWriter, r *http.Request, s *core.Session, req *restRequest) {
	data, err := readBody(r)
	if err != nil {
		writeRESTError(w, err)
		return
	}
	if _, err := core.ParsePath(req.path); err != nil {
		writeRESTError(w, err)
		return
	}
	id := a.pushIDs.Next(a.svc.Now())
	path := strings.TrimSuffix(req.path, "/") + "/" + id
	n, err := await(ctx, func(done func(*core.Node, error)) { s.Set(path, data, nil, done) })
	if err != nil {
		writeRESTError(w, dataError(err))
		return
	}
	if req.etag {
		w.Header().Set("ETag", etagOf(n))
		w.Header().Set("Access-Control-Expose-Headers", "ETag")
	}
	name, _ := json.Marshal(map[string]string{"name": id})
	writeRESTBody(w, http.StatusOK, name, req)
}

// errInvalidObject is the emulator's answer to a PATCH whose body is not an object of valid
// paths, or whose paths overlap.
var errInvalidObject = restErr(400, "Invalid data; couldn't parse JSON object. Are you sending a JSON object with valid key names?")

// restUpdate answers a PATCH: a multi-path update, echoed as the children it wrote, each at
// its path, server values resolved and nulls kept (emulator 4.11.2).
func (a *API) restUpdate(ctx context.Context, w http.ResponseWriter, r *http.Request, s *core.Session, req *restRequest) {
	if _, err := core.ParsePath(req.path); err != nil {
		writeRESTError(w, err)
		return
	}
	data, err := readBody(r)
	if err != nil {
		writeRESTError(w, err)
		return
	}
	if string(bytes.TrimSpace(data)) == "null" {
		// The emulator takes a PATCH of null as a PUT of it.
		if _, err := await(ctx, func(done func(*core.Node, error)) { s.Set(req.path, data, nil, done) }); err != nil {
			writeRESTError(w, err)
			return
		}
		writeRESTBody(w, http.StatusOK, data, req)
		return
	}
	merge, err := await(ctx, func(done func([]core.Child, error)) { s.UpdateWritten(req.path, data, done) })
	switch {
	case errors.Is(err, core.ErrPathTooDeep), errors.Is(err, core.ErrPathTooLong),
		errors.Is(err, core.ErrInvalidPriority), errors.Is(err, core.ErrInvalidServerValue):
		err = core.ErrInvalidData
	case errors.Is(err, core.ErrInvalidData), errors.Is(err, core.ErrOverlappingPaths),
		errors.Is(err, core.ErrInvalidKey), errors.Is(err, core.ErrInvalidPath):
		err = errInvalidObject
	}
	if err != nil {
		writeRESTError(w, err)
		return
	}
	writeRESTBody(w, http.StatusOK, echoUpdate(merge), req)
}

// echoUpdate renders an update's children as one object, each child's value at its path.
func echoUpdate(merge []core.Child) []byte {
	type tree struct {
		keys  []string
		kids  map[string]*tree
		value []byte // set for a written child
	}
	root := &tree{kids: map[string]*tree{}}
	for _, c := range merge {
		at := root
		for _, k := range c.Path {
			next := at.kids[k]
			if next == nil {
				next = &tree{kids: map[string]*tree{}}
				at.kids[k] = next
				at.keys = append(at.keys, k)
			}
			at = next
		}
		at.value = c.Value.ExportJSON()
	}
	var write func(b []byte, t *tree) []byte
	write = func(b []byte, t *tree) []byte {
		if t.value != nil {
			return append(b, t.value...)
		}
		b = append(b, '{')
		for i, k := range t.keys {
			if i > 0 {
				b = append(b, ',')
			}
			name, _ := json.Marshal(k)
			b = append(b, name...)
			b = append(b, ':')
			b = write(b, t.kids[k])
		}
		return append(b, '}')
	}
	return write(nil, root)
}

// readBody reads a write's body. The entry point caps it at rest_write_bytes.
func readBody(r *http.Request) (json.RawMessage, error) {
	b, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return nil, restErr(http.StatusBadRequest, core.ErrTooBig.Message)
	case err != nil:
		return nil, core.ErrInvalidData
	case len(b) == 0:
		return nil, errNoData
	}
	return b, nil
}

var errNoData = restErr(http.StatusBadRequest, "No data supplied.")

// etagOf is a node's ETag: its hash, the h of a conditional put (hash.go).
func etagOf(n *core.Node) string {
	if h := core.Hash(n); h != "" {
		return h
	}
	return nullETag
}

func setRESTHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
}

// writeRESTBody writes a successful answer: 204 with print=silent, the body otherwise,
// indented with print=pretty.
func writeRESTBody(w http.ResponseWriter, status int, body []byte, req *restRequest) {
	setRESTHeaders(w)
	if req.silent && status == http.StatusOK {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if req.pretty {
		// Production and the emulator end a pretty body with a newline.
		body = append(pretty(body), '\n')
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeRESTError answers with err: a restError as it is, a core error with its REST status
// and text. The texts of path errors gain "Invalid path: ", as the emulator's do.
func writeRESTError(w http.ResponseWriter, err error) {
	var re *restError
	if errors.As(err, &re) {
		writeError(w, re.status, re.msg)
		return
	}
	e := core.AsError(err)
	switch e {
	case core.ErrInvalidPath, core.ErrPathTooDeep, core.ErrPathTooLong:
		writeError(w, http.StatusBadRequest, "Invalid path: "+e.Message)
		return
	case core.ErrTooBig:
		// REST answers an oversized write with 400 (REST reference, "Error Conditions").
		writeError(w, http.StatusBadRequest, e.Message)
		return
	}
	writeError(w, e.HTTP, e.Message)
}

// writeError answers in Firebase's REST error shape, {"error" : "..."}, pretty-printed and
// ending in a newline as the emulator and production print it.
func writeError(w http.ResponseWriter, status int, msg string) {
	setRESTHeaders(w)
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = w.Write(append(pretty(b), '\n'))
}

// pretty indents compact JSON as print=pretty does, in the style of the emulator's Jackson
// printer: objects one member a line, two spaces a level, "key" : value; arrays on one line,
// [ a, b ].
func pretty(src []byte) []byte {
	out := make([]byte, 0, len(src)*2)
	// stack holds, for each open container, whether it is an object.
	var stack []bool
	inString, escaped := false, false
	// Only objects indent: Jackson's array indenter is inline, so arrays add no level.
	newline := func() {
		out = append(out, '\n')
		for _, obj := range stack {
			if obj {
				out = append(out, ' ', ' ')
			}
		}
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			out = append(out, c)
		case '{', '[':
			obj := c == '{'
			stack = append(stack, obj)
			if i+1 < len(src) && (src[i+1] == '}' || src[i+1] == ']') {
				out = append(out, c, ' ')
				continue
			}
			out = append(out, c)
			if obj {
				newline()
			} else {
				out = append(out, ' ')
			}
		case '}', ']':
			obj := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if obj && src[i-1] != '{' {
				newline()
			} else if !obj && src[i-1] != '[' {
				out = append(out, ' ')
			}
			out = append(out, c)
		case ',':
			out = append(out, ',')
			if stack[len(stack)-1] {
				newline()
			} else {
				out = append(out, ' ')
			}
		case ':':
			out = append(out, ' ', ':', ' ')
		default:
			out = append(out, c)
		}
	}
	return out
}

// The rules document. Firebase's tools and the SDKs' own test suites load rules through it
// before anything else.

// maxRulesBody caps a rules document sent over REST.
const maxRulesBody = 256 << 10

// serveRules answers GET and PUT /.settings/rules.json, as the emulator and firebase-tools use
// it: an admin credential in Authorization (Bearer), the document as JSON (comments allowed),
// {"status":"ok"} after a PUT, the document as stored after a GET. ?format=strict reads it as
// plain JSON, comments dropped (the Admin SDKs' getRulesJSON); ?dryRun=true checks a PUT's
// document and saves nothing (firebase deploy --dry-run, rtdb.ts:47).
func (a *API) serveRules(w http.ResponseWriter, r *http.Request, t core.Target) {
	// Only an admin credential may read or change rules. The emulator answers a request
	// without a credential with 403, and one with a user's with 401. An admin credential that
	// asks to act as a client, auth_variable_override, is no admin here: it gets the 401 the
	// emulator 4.11.2 gives it (probe of 2026-10-21), whatever the override holds. No Admin
	// SDK sends one to this path.
	token, _ := restCredential(r)
	switch {
	case r.URL.Query().Has("auth_variable_override"):
		writeError(w, http.StatusUnauthorized, "Permission denied")
		return
	case token == "":
		writeError(w, http.StatusForbidden, "Permission denied.")
		return
	case !isAdminToken(token):
		writeError(w, http.StatusUnauthorized, "Permission denied")
		return
	}
	if _, err := a.verifyAdmin(r.Context(), t, token); err != nil {
		if !refused(err) {
			writeError(w, http.StatusServiceUnavailable, core.ErrUnavailable.Message)
			return
		}
		writeError(w, http.StatusUnauthorized, "Unauthorized request.")
		return
	}
	q := r.URL.Query()
	switch r.Method {
	case http.MethodGet:
		doc, err := a.svc.Rules(r.Context(), t)
		if err != nil {
			e := core.AsError(err)
			writeError(w, e.HTTP, e.Message)
			return
		}
		body := []byte(doc)
		if q.Get("format") == "strict" {
			if body, err = rules.StrictJSON(body); err != nil {
				// Saving checks the document, so only a database edited by hand gets here.
				writeError(w, http.StatusInternalServerError, "The stored rules do not parse: "+err.Error())
				return
			}
			body = pretty(body)
		}
		setRESTHeaders(w)
		_, _ = w.Write(body)
	case http.MethodPut:
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRulesBody))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "Rules are too large.")
			return
		}
		if q.Get("dryRun") == "true" {
			err = a.svc.CheckRules(string(b))
		} else {
			err = a.svc.SetRules(r.Context(), t, string(b))
		}
		if err != nil {
			e := core.AsError(err)
			writeError(w, e.HTTP, e.Message)
			return
		}
		setRESTHeaders(w)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "Unsupported request type")
	}
}
