package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The golden exchanges are what the official clients and servers put on the wire, recorded
// outside this package (test/oracle/storage/README.md):
//
//   - emulator/: the Storage emulator of firebase-tools 15.34.0, captured 2026-10-23 (run
//     s0c-20261023-emu) with firebase 13.1.0, firebase-admin 14.5.0 and @google-cloud/storage
//     8.4.0, and firebase-admin-go 4.22.0 with cloud.google.com/go/storage 1.62.1;
//   - production/bucket_probe.json: anonymous GETs against production on 2026-10-23, the only
//     raw production bodies there are (no default bucket exists, README);
//   - Pyric's observations of real Storage (github.com/davideast/pyric, commit 756d23e,
//     Apache-2.0), quoted where a test uses them, since they are summaries, not bodies.
//
// The tests hold the package to them: every recorded /v0 resource decodes into its type with no
// field left over and encodes back to the same bytes, every GCS resource to the same JSON, and
// every recorded path, command, range, checksum and multipart body parses to what the server
// answered. Where the emulator is known to differ from production (E), the test says so.

const oracleDir = "../../../../test/oracle/storage"

type capture struct {
	Scenario string `json:"scenario"`
	Source   string `json:"source"`
	Version  string `json:"version"`
	Captured string `json:"captured"`
	Run      string `json:"run"`
	Bucket   string `json:"bucket"`
	Steps    []struct {
		Step      string     `json:"step"`
		Exchanges []exchange `json:"exchanges"`
	} `json:"steps"`

	file string
}

type exchange struct {
	Request struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    *body             `json:"body"`
	} `json:"request"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    *body             `json:"body"`
	} `json:"response"`

	file, step string
}

// body is a recorded body: text, small binary as base64, or a large one by its digest only.
type body struct {
	Text   *string `json:"text"`
	Base64 *string `json:"base64"`
	Length int     `json:"length"`
}

// bytes returns the whole body, or false when only its digest was recorded.
func (b *body) bytes() ([]byte, bool) {
	switch {
	case b == nil:
		return nil, true
	case b.Text != nil:
		return []byte(*b.Text), true
	case b.Base64 != nil:
		v, err := base64.StdEncoding.DecodeString(*b.Base64)
		return v, err == nil
	}
	return nil, false
}

// header builds the http.Header a server would have parsed from a recorded header map.
func header(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}

const (
	emulatorVersion = "firebase-tools 15.34.0 Storage emulator (cloud-storage-rules-runtime-v1.1.3.jar, OpenJDK 21)"
	emulatorRun     = "s0c-20261023-emu"
	emulatorBucket  = "demo-aruma-ref.appspot.com"
)

func loadCaptures(t *testing.T) []capture {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(oracleDir, "emulator", "*.json"))
	if err != nil || len(files) != 13 {
		t.Fatalf("emulator captures: %v, %d files", err, len(files))
	}
	var out []capture
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var c capture
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if c.Source != "emulator" || c.Version != emulatorVersion || c.Captured != "2026-10-23" ||
			c.Run != emulatorRun || c.Bucket != emulatorBucket {
			t.Fatalf("%s: provenance %q %q %q %q %q", f, c.Source, c.Version, c.Captured, c.Run, c.Bucket)
		}
		c.file = filepath.Base(f)
		out = append(out, c)
	}
	return out
}

// exchanges returns every recorded exchange, tagged with its file and step.
func exchanges(t *testing.T) []exchange {
	t.Helper()
	var out []exchange
	for _, c := range loadCaptures(t) {
		for _, s := range c.Steps {
			for _, e := range s.Exchanges {
				e.file, e.step = c.file, s.Step
				out = append(out, e)
			}
		}
	}
	if len(out) < 230 {
		t.Fatalf("only %d exchanges", len(out))
	}
	return out
}

// jsonReply returns a reply's JSON object, or nil when the reply is not one.
func (e exchange) jsonReply() []byte {
	b, ok := e.Response.Body.bytes()
	if !ok || len(b) == 0 || b[0] != '{' || !json.Valid(b) {
		return nil
	}
	return b
}

func (e exchange) String() string {
	return e.file + ": " + e.step + ": " + e.Request.Method + " " + e.Request.URL
}

// strictDecode decodes b into v refusing any field v has no place for.
func strictDecode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// sameJSON reports whether a and b are the same JSON value, whatever their key order.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("%v: %s", err, a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return reflect.DeepEqual(x, y)
}

// sortedMaps reports whether every object nested below the top level has its keys in Go's
// order, so a byte comparison is fair: Go writes a map's keys sorted, the emulator in insertion
// order, and no client reads custom metadata in order.
func sortedMaps(b []byte) bool {
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil {
		return false
	}
	for _, raw := range top {
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.Token()
		prev := ""
		for i := 0; dec.More(); i++ {
			k, _ := dec.Token()
			key, _ := k.(string)
			if i > 0 && key < prev {
				return false
			}
			prev = key
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return false
			}
		}
	}
	return true
}

// topKeys returns an object's keys in the order written.
func topKeys(b []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.Token()
	var keys []string
	for dec.More() {
		k, _ := dec.Token()
		keys = append(keys, k.(string))
		var skip json.RawMessage
		dec.Decode(&skip)
	}
	return keys
}

func isV0(e exchange) bool { return strings.HasPrefix(e.Request.URL, FirebasePrefix) }

// Every /v0 resource the emulator wrote decodes into its type, with no field left over, and
// encodes back to the same bytes: same keys, same order, same strings.
func TestGoldenFirebaseResources(t *testing.T) {
	var objects, lists, errs int
	for _, e := range exchanges(t) {
		b := e.jsonReply()
		if b == nil || !isV0(e) {
			continue
		}
		var v any
		switch {
		case bytes.HasPrefix(b, []byte(`{"error"`)):
			v, errs = new(FirebaseError), errs+1
		case bytes.Contains(b, []byte(`"items":`)):
			v, lists = new(FirebaseList), lists+1
		default:
			v, objects = new(FirebaseObject), objects+1
		}
		if err := strictDecode(b, v); err != nil {
			t.Errorf("%v: %v\n%s", e, err, b)
			continue
		}
		got, err := Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		// The emulator writes an empty custom metadata as {}; every field of a /v0 type is
		// omitempty (doc.go), so it is absent here. The SDKs read both as no custom metadata.
		b = bytes.Replace(b, []byte(`,"metadata":{}`), nil, 1)
		if !reflect.DeepEqual(topKeys(got), topKeys(b)) || !sameJSON(t, got, b) {
			t.Errorf("%v:\n got %s\nwant %s", e, got, b)
		} else if sortedMaps(b) && !bytes.Equal(got, b) {
			t.Errorf("%v: not byte for byte:\n got %s\nwant %s", e, got, b)
		}
	}
	if objects < 50 || lists < 10 || errs < 30 {
		t.Errorf("only %d objects, %d lists, %d errors", objects, lists, errs)
	}
}

// Every GCS resource the emulator wrote decodes into its type, with no field left over, and
// encodes back to the same JSON (the key order of GCS replies is not recorded from production).
func TestGoldenGCSResources(t *testing.T) {
	kinds := map[string]int{}
	for _, e := range exchanges(t) {
		b := e.jsonReply()
		if b == nil || isV0(e) {
			continue
		}
		var probe struct {
			Kind string `json:"kind"`
		}
		json.Unmarshal(b, &probe)
		var v any
		switch probe.Kind {
		case KindObject:
			v = new(GCSObject)
		case KindObjects:
			v = new(GCSObjects)
		case KindRewrite:
			v = new(GCSRewrite)
		case "":
			v, probe.Kind = new(GCSError), "error"
		default:
			t.Errorf("%v: kind %q", e, probe.Kind)
			continue
		}
		kinds[probe.Kind]++
		if err := strictDecode(b, v); err != nil {
			t.Errorf("%v: %v\n%s", e, err, b)
			continue
		}
		got, err := Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, got, b) {
			t.Errorf("%v:\n got %s\nwant %s", e, got, b)
		}
	}
	if kinds[KindObject] < 10 || kinds[KindObjects] < 5 || kinds[KindRewrite] < 2 || kinds["error"] < 10 {
		t.Errorf("too few resources: %v", kinds)
	}
}

// Production's error bodies (bucket_probe.json) are what NewFirebaseError and NewGCSError write,
// with production's messages. Production indents its JSON; no client reads the whitespace.
func TestGoldenProductionErrors(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(oracleDir, "production", "bucket_probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Source   string `json:"source"`
		Captured string `json:"captured"`
		Probes   []struct {
			Control  string `json:"control"`
			Request  struct{ Method, URL string }
			Response struct {
				Status int    `json:"status"`
				Body   string `json:"body"`
			}
		} `json:"probes"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.Source != "production" || probe.Captured != "2026-10-23" {
		t.Fatalf("provenance %q %q", probe.Source, probe.Captured)
	}
	var v0, gcs int
	for _, p := range probe.Probes {
		if p.Control != "" { // the public bucket's 200s, recorded truncated
			continue
		}
		var want any
		switch {
		case strings.Contains(p.Request.URL, "/v0/"):
			msg := MessageNotFound
			if p.Response.Status == http.StatusBadRequest {
				msg = MessageBadMethodOrURL
			}
			want, v0 = NewFirebaseError(p.Response.Status, msg), v0+1
		default:
			want, gcs = NewGCSError(p.Response.Status, ReasonNotFound, MessageNoSuchBucket), gcs+1
		}
		got, err := Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, got, []byte(p.Response.Body)) {
			t.Errorf("%s %s:\n got %s\nwant %s", p.Request.Method, p.Request.URL, got, p.Response.Body)
		}
	}
	if v0 < 8 || gcs < 4 {
		t.Errorf("only %d /v0 and %d GCS errors", v0, gcs)
	}
}

// The emulator's GCS 404 for an object is NewGCSError with MessageNoSuchObject.
func TestGoldenGCSNoSuchObject(t *testing.T) {
	n := 0
	for _, e := range exchanges(t) {
		b := e.jsonReply()
		if b == nil || isV0(e) || e.Response.Status != http.StatusNotFound ||
			!strings.HasPrefix(e.Request.URL, GCSPrefix+"b/") || !strings.Contains(e.Request.URL, "/o/") {
			continue
		}
		path, _, _ := strings.Cut(strings.TrimPrefix(e.Request.URL, GCSPrefix), "?")
		p, err := SplitObjectPath(path)
		if err != nil {
			t.Fatalf("%v: %v", e, err)
		}
		want, _ := Marshal(NewGCSError(404, ReasonNotFound, MessageNoSuchObject(p.Bucket, p.Object)))
		if !sameJSON(t, want, b) {
			t.Errorf("%v:\n got %s\nwant %s", e, want, b)
		}
		n++
	}
	if n < 2 {
		t.Errorf("only %d", n)
	}
}

// The links and ids of the emulator's GCS objects are what GCSSelfLink, GCSMediaLink and
// GCSObjectID write from the emulator's own address.
func TestGoldenGCSLinks(t *testing.T) {
	const base = "http://127.0.0.1:8860"
	n := 0
	for _, e := range exchanges(t) {
		b := e.jsonReply()
		if b == nil || isV0(e) {
			continue
		}
		var objs []GCSObject
		var page GCSObjects
		var rw GCSRewrite
		var one GCSObject
		switch {
		case json.Unmarshal(b, &page) == nil && page.Kind == KindObjects:
			objs = page.Items
		case json.Unmarshal(b, &rw) == nil && rw.Kind == KindRewrite:
			objs = []GCSObject{*rw.Resource}
		case json.Unmarshal(b, &one) == nil && one.Kind == KindObject:
			objs = []GCSObject{one}
		}
		for _, o := range objs {
			gen, err := ParseLength(o.Generation)
			if err != nil {
				t.Fatalf("%v: %v", e, err)
			}
			if got := GCSSelfLink(base, o.Bucket, o.Name); got != o.SelfLink {
				t.Errorf("selfLink %s, want %s", got, o.SelfLink)
			}
			if got := GCSMediaLink(base, o.Bucket, o.Name, gen); got != o.MediaLink {
				t.Errorf("mediaLink %s, want %s", got, o.MediaLink)
			}
			if got := GCSObjectID(o.Bucket, o.Name, gen); got != o.ID {
				t.Errorf("id %s, want %s", got, o.ID)
			}
			if got := Int(gen); got != o.Generation {
				t.Errorf("Int %s, want %s", got, o.Generation)
			}
			n++
		}
	}
	if n < 20 {
		t.Errorf("only %d objects", n)
	}
}

// The session URLs the emulator gave are what FirebaseUploadURL and GCSUploadURL write, from its
// public address (the proxy's, as clients saw it). The recorded ids are placeholders with "<>",
// which no real id has, so the test puts a plain one in their place.
func TestGoldenUploadURLs(t *testing.T) {
	const base = "http://127.0.0.1:8865"
	var v0, gcs int
	for _, e := range exchanges(t) {
		h := header(e.Response.Headers)
		if u := h.Get(UploadURL); u != "" {
			name := queryValue(t, u, "name")
			want := replaceID(u, queryValue(t, u, "upload_id"))
			if got := FirebaseUploadURL(base, emulatorBucket, name, "ID"); got != want {
				t.Errorf("%v:\n got %s\nwant %s", e, got, want)
			}
			v0++
		}
		if u := h.Get("Location"); u != "" && strings.Contains(u, GCSUploadPrefix) {
			name := queryValue(t, u, "name")
			got := GCSUploadURL(base, emulatorBucket, name, "ID")
			if !sameURL(t, got, replaceID(u, queryValue(t, u, "upload_id"))) {
				t.Errorf("%v:\n got %s\nwant %s", e, got, u)
			}
			gcs++
		}
	}
	if v0 < 5 || gcs < 4 {
		t.Errorf("only %d /v0 and %d GCS session URLs", v0, gcs)
	}
}

func queryValue(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(key)
}

// replaceID puts the plain id "ID" in place of a recorded placeholder.
func replaceID(raw, id string) string {
	return strings.Replace(raw, "upload_id="+id, "upload_id=ID", 1)
}

// sameURL compares two URLs by scheme, host, path and query values, whatever the order of the
// query's parameters.
func sameURL(t *testing.T, a, b string) bool {
	t.Helper()
	x, err := url.Parse(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := url.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return x.Scheme == y.Scheme && x.Host == y.Host && x.EscapedPath() == y.EscapedPath() &&
		reflect.DeepEqual(x.Query(), y.Query())
}
