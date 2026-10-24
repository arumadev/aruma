package rulestest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// The binding: how a case becomes the engine's input. The services bind real requests in their
// own packages (S2, F5); this binding is the test's, and it reproduces what the case's source
// gave its rules, so that a disagreement is the language's and not the binding's:
//
//   - rules-test-api: request and resource are literal maps, as projects.test takes them. A field
//     that was not sent is absent from the map (undefined in the rules) and null is a value. JSON
//     is read as the Test API reads it: a whole number is an int even when written 2.0 or 1e2, a
//     string in RFC 3339 form is a timestamp, one that starts with /databases/ a path, and a map
//     of exactly latitude and longitude a latlng (parity-exceptions.md, "Test API artefacts").
//     Mocked lookups answer the mock's value as given, without id or __name__; a lookup that is
//     not mocked, or whose mock is undefined, fails as rules.ErrNoDocuments, which is the
//     evaluation error the Test API reports ("Function not found", "Service call error").
//   - emulator: what the emulator recorder (test/oracle/rules/tools/record-emulator.mjs) and the
//     emulator made of the case. Firestore: request.auth is null without a user and holds the
//     unsigned ID token's claims with one; request.resource is the written document on create
//     and update and absent otherwise; resource is the stored document, with id and __name__, or
//     null; numbers are ints when whole (JavaScript's Number.isInteger) and strings stay strings.
//     Storage (cloud-storage-rules-runtime, as firebase-tools drives it): request has path, time,
//     resource and auth and no method; timeCreated and updated are timestamps. Mocked documents
//     are stored documents, and a document that is not stored is not found.
//   - firestore-rest (hidane): the emulator binding, with the document's values typed as
//     Firestore REST writes them.
//
// A case that the binding cannot give the engine faithfully is a *NotExpressibleError.

// Project is the project number every oracle request is made for.
const Project int64 = 1

// emulatorProject is the project the emulator recorder used: the ID token's audience, and the
// prefix of the names the Storage emulator gives documents.
const emulatorProject = "demo-oracle"

// NotExpressibleError says why a case cannot be given to the engine as its source saw it.
type NotExpressibleError struct{ Reason string }

func (e *NotExpressibleError) Error() string { return "rulestest: not expressible: " + e.Reason }

func notExpressible(format string, args ...any) error {
	return &NotExpressibleError{Reason: fmt.Sprintf(format, args...)}
}

// Method returns the engine's method for a request's method name.
func Method(name string) (rules.Method, bool) {
	switch name {
	case "get":
		return rules.MethodGet, true
	case "list":
		return rules.MethodList, true
	case "create":
		return rules.MethodCreate, true
	case "update":
		return rules.MethodUpdate, true
	case "delete":
		return rules.MethodDelete, true
	}
	return 0, false
}

// Segments splits a request path, /databases/(default)/documents/... or /b/<bucket>/o/..., into
// the segments the match blocks see from the service root. Segments are used as written.
func Segments(path string) []string {
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

// Build returns the rules file and the request the case gives the engine, with a fresh Budget, a
// MemoryDocuments holding its mocks, the inflight cap given, and a trace of the statements.
func (c *Case) Build(inflight *rules.Inflight) (rules.File, *rules.Request, error) {
	if c.Ruleset == "" {
		return rules.File{}, nil, notExpressible("%s", c.NoRuleset)
	}
	file := rules.File{Name: c.FileName(), Content: c.Ruleset}
	if c.Request == nil {
		return file, nil, notExpressible("%s", c.NoRequest)
	}
	method, ok := Method(c.Request.Method)
	if !ok {
		return file, nil, notExpressible("the request method %q has no engine method", c.Request.Method)
	}
	if !strings.HasPrefix(c.Request.Path, "/") {
		return file, nil, notExpressible("the request path %q is not absolute", c.Request.Path)
	}
	// The Test API read the whole request as written, raw numbers included; the emulator
	// recorder wrote them into the documents' data alone (record-emulator.mjs, applyRaw).
	b := &binder{c: c, raw: c.Provenance != Emulator}
	var (
		vars map[string]rules.Value
		docs *MemoryDocuments
		err  error
	)
	if c.Provenance == Emulator {
		vars, err = b.emulatorVariables()
		if err == nil {
			docs, err = b.emulatorDocuments(vars)
		}
	} else {
		vars, err = b.testAPIVariables()
		if err == nil {
			docs, err = b.testAPIDocuments()
		}
	}
	if err != nil {
		return file, nil, err
	}
	req := &rules.Request{
		Project:   Project,
		Method:    method,
		Path:      rules.Literals(Segments(c.Request.Path)...),
		Variables: vars,
		Documents: docs,
		Budget:    rules.NewBudget(),
		Inflight:  inflight,
		Trace:     rules.TraceStatements,
	}
	return file, req, nil
}

type binder struct {
	c   *Case
	raw bool // apply the case's raw numbers: where the source read them as written
}

// decode reads a field's JSON, keeping numbers as their text.
func (b *binder) decode(f Field) (any, error) {
	d := json.NewDecoder(bytes.NewReader(f.Raw()))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// numberText returns the text a number had in the request: the case's raw text for a
// placeholder, else the JSON's own.
func (b *binder) numberText(n json.Number) (text string, raw bool) {
	if t, ok := b.c.RawNumbers[n.String()]; ok && b.raw {
		return t, true
	}
	return n.String(), false
}

// ---- rules-test-api ----

// rfc3339 is the form of a string the Test API reads as a timestamp.
var rfc3339 = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$`)

// TestAPIValue converts JSON as the Test API reads a request or a resource.
func (b *binder) testAPIValue(v any) (rules.Value, error) {
	switch x := v.(type) {
	case nil:
		return rules.Null(), nil
	case bool:
		return rules.Bool(x), nil
	case json.Number:
		text, _ := b.numberText(x)
		return testAPINumber(text)
	case string:
		if rfc3339.MatchString(x) {
			t, err := time.Parse(time.RFC3339Nano, x)
			if err == nil {
				return rules.Timestamp(t), nil
			}
		}
		if strings.HasPrefix(x, "/databases/") {
			return rules.PathOf(Segments(x)), nil
		}
		return rules.String(x), nil
	case []any:
		out := make([]rules.Value, len(x))
		for i, e := range x {
			ev, err := b.testAPIValue(e)
			if err != nil {
				return rules.Value{}, err
			}
			out[i] = ev
		}
		return rules.List(out), nil
	case map[string]any:
		if lat, lng, ok := latLngOf(x); ok {
			return rules.LatLng(lat, lng), nil
		}
		out := make(map[string]rules.Value, len(x))
		for k, e := range x {
			ev, err := b.testAPIValue(e)
			if err != nil {
				return rules.Value{}, err
			}
			out[k] = ev
		}
		return rules.Map(out), nil
	}
	return rules.Value{}, fmt.Errorf("rulestest: unexpected JSON value %T", v)
}

// testAPINumber reads a JSON number as the Test API does: as a double, which is an int when it
// is whole. Beyond 2^53 the double has already rounded (9007199254740993 reads as ...992), and
// 2^63 itself, which 9223372036854775807 rounds to, saturates to the largest int.
func testAPINumber(text string) (rules.Value, error) {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && !isRange(err) {
		return rules.Value{}, fmt.Errorf("rulestest: number %q: %w", text, err)
	}
	return wholeIsInt(f), nil
}

func isRange(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// wholeIsInt returns an int for a whole double within the int64 range (saturating at 2^63), and
// a float otherwise.
func wholeIsInt(f float64) rules.Value {
	const two63 = 9223372036854775808.0
	switch {
	case math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) || f > two63 || f < -two63:
		return rules.Float(f)
	case f == two63:
		return rules.Int(math.MaxInt64)
	}
	return rules.Int(int64(f))
}

func latLngOf(m map[string]any) (lat, lng float64, ok bool) {
	if len(m) != 2 {
		return 0, 0, false
	}
	la, ok1 := m["latitude"].(json.Number)
	lo, ok2 := m["longitude"].(json.Number)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	lat, err1 := la.Float64()
	lng, err2 := lo.Float64()
	return lat, lng, err1 == nil && err2 == nil
}

func (b *binder) testAPIField(f Field) (rules.Value, error) {
	v, err := b.decode(f)
	if err != nil {
		return rules.Value{}, err
	}
	return b.testAPIValue(v)
}

func (b *binder) testAPIVariables() (map[string]rules.Value, error) {
	r := b.c.Request
	req := map[string]rules.Value{
		"method": rules.String(r.Method),
		"path":   rules.PathOf(Segments(r.Path)),
	}
	if r.Time.Present() {
		var s string
		if err := json.Unmarshal(r.Time.Raw(), &s); err != nil || !rfc3339.MatchString(s) {
			return nil, notExpressible("request.time %s is not a timestamp: the Test API refused the request", r.Time.Raw())
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil, notExpressible("request.time %q: %v", s, err)
		}
		req["time"] = rules.Timestamp(t)
	}
	for name, f := range map[string]Field{"auth": r.Auth, "resource": r.Resource, "query": r.Query} {
		if !f.Present() {
			continue
		}
		v, err := b.testAPIField(f)
		if err != nil {
			return nil, fmt.Errorf("request.%s: %w", name, err)
		}
		req[name] = v
	}
	vars := map[string]rules.Value{"request": rules.Map(req)}
	if b.c.Resource.Present() {
		v, err := b.testAPIField(b.c.Resource)
		if err != nil {
			return nil, fmt.Errorf("resource: %w", err)
		}
		vars["resource"] = v
	}
	return vars, nil
}

// mockTarget splits a mock's function into what it reads: get or exists, and whether after the
// request's writes.
func mockTarget(fn string) (get, after bool, err error) {
	switch strings.TrimPrefix(fn, "firestore.") {
	case "get":
		return true, false, nil
	case "exists":
		return false, false, nil
	case "getAfter":
		return true, true, nil
	case "existsAfter":
		return false, true, nil
	}
	return false, false, fmt.Errorf("rulestest: unknown mocked function %q", fn)
}

func (b *binder) testAPIDocuments() (*MemoryDocuments, error) {
	docs := NewMemoryDocuments()
	docs.Missing = rules.ErrNoDocuments
	// exists() mocks first, so that a get() mock of the same document decides its value.
	for _, pass := range []bool{false, true} {
		for _, m := range b.c.Mocks {
			get, after, err := mockTarget(m.Function)
			if err != nil {
				return nil, err
			}
			if get != pass {
				continue
			}
			switch {
			case !m.Result.Present():
				docs.Fail(Project, m.Path, after, rules.ErrNoDocuments)
			case get && m.Result.IsNull():
				docs.Absent(Project, m.Path, after)
			case get:
				v, err := b.testAPIField(m.Result)
				if err != nil {
					return nil, fmt.Errorf("mock of %s: %w", m.Path, err)
				}
				docs.Put(Project, m.Path, after, v)
			default:
				var found bool
				if err := json.Unmarshal(m.Result.Raw(), &found); err != nil {
					return nil, fmt.Errorf("mock of %s: exists() result %s", m.Path, m.Result.Raw())
				}
				if found {
					docs.Put(Project, m.Path, after, rules.Map(nil))
				} else {
					docs.Absent(Project, m.Path, after)
				}
			}
		}
	}
	return docs, nil
}

// ---- emulator ----

// jsValue converts JSON as JavaScript reads it and the recorder wrote it to the emulator: a
// whole number is an int, other numbers floats; a raw number is a float when its text has
// '.', 'e' or 'E'; strings stay strings.
func (b *binder) jsValue(v any) (rules.Value, error) {
	switch x := v.(type) {
	case nil:
		return rules.Null(), nil
	case bool:
		return rules.Bool(x), nil
	case json.Number:
		text, raw := b.numberText(x)
		if raw && !strings.ContainsAny(text, ".eE") {
			i, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return rules.Value{}, fmt.Errorf("rulestest: raw number %q: %w", text, err)
			}
			return rules.Int(i), nil
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil && !isRange(err) {
			return rules.Value{}, fmt.Errorf("rulestest: number %q: %w", text, err)
		}
		if raw {
			return rules.Float(f), nil
		}
		return wholeIsInt(f), nil
	case string:
		return rules.String(x), nil
	case []any:
		out := make([]rules.Value, len(x))
		for i, e := range x {
			ev, err := b.jsValue(e)
			if err != nil {
				return rules.Value{}, err
			}
			out[i] = ev
		}
		return rules.List(out), nil
	case map[string]any:
		out := make(map[string]rules.Value, len(x))
		for k, e := range x {
			ev, err := b.jsValue(e)
			if err != nil {
				return rules.Value{}, err
			}
			out[k] = ev
		}
		return rules.Map(out), nil
	}
	return rules.Value{}, fmt.Errorf("rulestest: unexpected JSON value %T", v)
}

// restValue converts a Firestore REST typed value.
func restValue(v any) (rules.Value, error) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return rules.Value{}, fmt.Errorf("rulestest: not a Firestore REST value: %v", v)
	}
	for k, x := range m {
		switch k {
		case "nullValue":
			return rules.Null(), nil
		case "booleanValue":
			b, _ := x.(bool)
			return rules.Bool(b), nil
		case "integerValue":
			s := fmt.Sprint(x)
			i, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return rules.Value{}, fmt.Errorf("rulestest: integerValue %q: %w", s, err)
			}
			return rules.Int(i), nil
		case "doubleValue":
			var f float64
			switch d := x.(type) {
			case json.Number:
				f, _ = d.Float64()
			case string: // NaN, Infinity, -Infinity
				f, _ = strconv.ParseFloat(d, 64)
			}
			return rules.Float(f), nil
		case "timestampValue":
			s, _ := x.(string)
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return rules.Value{}, fmt.Errorf("rulestest: timestampValue %q: %w", s, err)
			}
			return rules.Timestamp(t), nil
		case "stringValue":
			s, _ := x.(string)
			return rules.String(s), nil
		case "bytesValue":
			s, _ := x.(string)
			raw, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return rules.Value{}, fmt.Errorf("rulestest: bytesValue %q: %w", s, err)
			}
			return rules.Bytes(raw), nil
		case "referenceValue":
			s, _ := x.(string)
			i := strings.Index(s, "/databases/")
			if i < 0 {
				return rules.Value{}, fmt.Errorf("rulestest: referenceValue %q", s)
			}
			return rules.PathOf(Segments(s[i:])), nil
		case "geoPointValue":
			g, _ := x.(map[string]any)
			lat, _ := g["latitude"].(json.Number)
			lng, _ := g["longitude"].(json.Number)
			la, _ := lat.Float64()
			lo, _ := lng.Float64()
			return rules.LatLng(la, lo), nil
		case "arrayValue":
			a, _ := x.(map[string]any)
			vs, _ := a["values"].([]any)
			out := make([]rules.Value, len(vs))
			for i, e := range vs {
				ev, err := restValue(e)
				if err != nil {
					return rules.Value{}, err
				}
				out[i] = ev
			}
			return rules.List(out), nil
		case "mapValue":
			mv, _ := x.(map[string]any)
			fields, _ := mv["fields"].(map[string]any)
			return restFields(fields)
		}
		return rules.Value{}, fmt.Errorf("rulestest: unknown Firestore REST value %q", k)
	}
	panic("unreachable")
}

func restFields(fields map[string]any) (rules.Value, error) {
	out := make(map[string]rules.Value, len(fields))
	for k, e := range fields {
		ev, err := restValue(e)
		if err != nil {
			return rules.Value{}, err
		}
		out[k] = ev
	}
	return rules.Map(out), nil
}

// emulatorData converts a document's data, written as the case's encoding writes it, with
// the case's raw numbers when raw is set.
func (b *binder) emulatorData(v any, raw bool) (rules.Value, error) {
	prev := b.raw
	b.raw = raw
	defer func() { b.raw = prev }()
	if b.c.Encoding == EncodingFirestoreREST {
		fields, _ := v.(map[string]any)
		return restFields(fields)
	}
	if v == nil {
		return rules.Map(nil), nil
	}
	return b.jsValue(v)
}

// document returns a Firestore document as the emulator gives it to rules: data, id, and
// __name__, the document's path after the prefix given.
func document(path string, data rules.Value, prefix ...string) rules.Value {
	segs := Segments(path)
	return rules.Map(map[string]rules.Value{
		"data":     data,
		"id":       rules.String(segs[len(segs)-1]),
		"__name__": rules.PathOf(append(prefix, segs...)),
	})
}

// emulatorAuth is request.auth as the Firestore emulator builds it from the recorder's unsigned
// ID token: the token's standard claims, then the case's.
func (b *binder) emulatorAuth(now time.Time) (rules.Value, error) {
	r := b.c.Request
	if !r.Auth.Present() || r.Auth.IsNull() {
		return rules.Null(), nil
	}
	v, err := b.decode(r.Auth)
	if err != nil {
		return rules.Value{}, err
	}
	a, _ := v.(map[string]any)
	uid, ok := a["uid"].(string)
	token, _ := a["token"].(map[string]any)
	if !ok || token == nil {
		return rules.Value{}, notExpressible("the auth %s cannot be an ID token", r.Auth.Raw())
	}
	claims := map[string]rules.Value{}
	if b.c.Service == rules.Firestore {
		sec := rules.Int(now.Unix())
		claims["iss"] = rules.String("https://securetoken.google.com/" + emulatorProject)
		claims["aud"] = rules.String(emulatorProject)
		claims["iat"], claims["exp"], claims["auth_time"] = sec, rules.Int(now.Unix()+3600), sec
		claims["sub"], claims["user_id"] = rules.String(uid), rules.String(uid)
	}
	for k, x := range token {
		cv, err := b.jsValue(x)
		if err != nil {
			return rules.Value{}, err
		}
		claims[k] = cv
	}
	return rules.Map(map[string]rules.Value{"uid": rules.String(uid), "token": rules.Map(claims)}), nil
}

// emulatorTime is request.time on the emulator: the case's, or the emulator's clock. The
// recorder skipped the Firestore cases that read request.time.
func (b *binder) emulatorTime() (time.Time, error) {
	r := b.c.Request
	if !r.Time.Present() || r.Time.IsNull() {
		return time.Now().UTC(), nil
	}
	var s string
	if err := json.Unmarshal(r.Time.Raw(), &s); err != nil {
		return time.Time{}, notExpressible("request.time %s", r.Time.Raw())
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, notExpressible("request.time %q: %v", s, err)
	}
	return t, nil
}

// storageObject converts a Storage object as firebase-tools gives it to the rules runtime:
// timeCreated and updated are timestamps.
func (b *binder) storageObject(f Field) (rules.Value, error) {
	if !f.Present() || f.IsNull() {
		return rules.Null(), nil
	}
	v, err := b.decode(f)
	if err != nil {
		return rules.Value{}, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return b.jsValue(v)
	}
	out := make(map[string]rules.Value, len(obj))
	for k, x := range obj {
		if s, ok := x.(string); ok && (k == "timeCreated" || k == "updated") {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				out[k] = rules.Timestamp(t)
				continue
			}
		}
		xv, err := b.jsValue(x)
		if err != nil {
			return rules.Value{}, err
		}
		out[k] = xv
	}
	return rules.Map(out), nil
}

func (b *binder) emulatorVariables() (map[string]rules.Value, error) {
	r := b.c.Request
	now, err := b.emulatorTime()
	if err != nil {
		return nil, err
	}
	auth, err := b.emulatorAuth(now)
	if err != nil {
		return nil, err
	}
	if b.c.Service == rules.Storage {
		reqRes, err := b.storageObject(r.Resource)
		if err != nil {
			return nil, err
		}
		res, err := b.storageObject(b.c.Resource)
		if err != nil {
			return nil, err
		}
		return map[string]rules.Value{
			"request": rules.Map(map[string]rules.Value{
				"path":     rules.PathOf(Segments(r.Path)),
				"time":     rules.Timestamp(now),
				"resource": reqRes,
				"auth":     auth,
			}),
			"resource": res,
		}, nil
	}

	req := map[string]rules.Value{
		"method": rules.String(r.Method),
		"path":   rules.PathOf(Segments(r.Path)),
		"time":   rules.Timestamp(now),
		"auth":   auth,
	}
	if r.Method == "create" || r.Method == "update" {
		var data any
		if r.Resource.Present() && !r.Resource.IsNull() {
			v, err := b.decode(r.Resource)
			if err != nil {
				return nil, err
			}
			if m, ok := v.(map[string]any); ok {
				data = m["data"]
			}
		}
		dv, err := b.emulatorData(data, true)
		if err != nil {
			return nil, err
		}
		req["resource"] = document(r.Path, dv)
	}
	res := rules.Null()
	if b.c.Resource.Present() && !b.c.Resource.IsNull() {
		v, err := b.decode(b.c.Resource)
		if err != nil {
			return nil, err
		}
		m, _ := v.(map[string]any)
		if data, ok := m["data"]; ok {
			dv, err := b.emulatorData(data, true)
			if err != nil {
				return nil, err
			}
			res = document(r.Path, dv)
		}
	}
	return map[string]rules.Value{"request": rules.Map(req), "resource": res}, nil
}

// emulatorDocuments returns the documents the emulator held when the case ran, as the recorder
// wrote them, in its order: each mock in turn (get() with data and a true exists() write the
// document; anything else, getAfter() and existsAfter() included, deletes it), then the
// request's own document, stored or deleted. Reads after the request's writes see the same
// documents, except the request's own, which a create or an update writes and a delete removes.
func (b *binder) emulatorDocuments(vars map[string]rules.Value) (*MemoryDocuments, error) {
	docs := NewMemoryDocuments()
	both := func(path string, doc rules.Value, found bool) {
		for _, after := range []bool{false, true} {
			if found {
				docs.Put(Project, path, after, doc)
			} else {
				docs.Absent(Project, path, after)
			}
		}
	}
	var prefix []string
	if b.c.Service == rules.Storage {
		// The Storage emulator names documents as the Firestore emulator sends them.
		prefix = []string{"projects", emulatorProject}
	}
	for _, m := range b.c.Mocks {
		fn := strings.TrimPrefix(m.Function, "firestore.")
		var data any
		stored := false
		if (fn == "get" || fn == "exists") && m.Result.Present() && !m.Result.IsNull() {
			v, err := b.decode(m.Result)
			if err != nil {
				return nil, err
			}
			if fn == "get" {
				if mv, ok := v.(map[string]any); ok {
					data, stored = mv["data"], mv["data"] != nil
				}
			} else {
				stored = v == true
			}
		}
		if !stored {
			both(m.Path, rules.Value{}, false)
			continue
		}
		dv, err := b.emulatorData(data, false)
		if err != nil {
			return nil, err
		}
		both(m.Path, document(m.Path, dv, prefix...), true)
	}
	if b.c.Service != rules.Firestore {
		return docs, nil
	}
	path := b.c.Request.Path
	res := vars["resource"]
	both(path, res, res.Kind() == rules.KindMap)
	req, _ := vars["request"].AsMap()
	switch b.c.Request.Method {
	case "create", "update":
		docs.Put(Project, path, true, req["resource"])
	case "delete":
		docs.Absent(Project, path, true)
	}
	return docs, nil
}

// TestAPIValue converts JSON as the Test API reads a request or a resource (see Build).
func TestAPIValue(raw []byte) (rules.Value, error) {
	b := &binder{c: &Case{}}
	return b.testAPIField(Field{raw: raw})
}
