// Package documents checks the Document Database oracle: the recorded answers in corpus/, the
// manifest that pins them, and parity-exceptions.json. It reads files only, so it runs in
// go test ./... without network, Java or Node. README.md says how the files are made.
package documents

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type manifestEntry struct {
	SHA256     string            `json:"sha256"`
	Oracle     string            `json:"oracle"`
	Generator  string            `json:"generator"`
	RecordedAt string            `json:"recordedAt"`
	Versions   map[string]string `json:"versions"`
}

type manifest struct {
	Schema string                   `json:"schema"`
	Files  map[string]manifestEntry `json:"files"`
}

type corpusHeader struct {
	Oracle     string            `json:"oracle"`
	Generator  string            `json:"generator"`
	RecordedAt string            `json:"recordedAt"`
	Versions   map[string]string `json:"versions"`
}

type corpusFile struct {
	Schema string            `json:"schema"`
	Header corpusHeader      `json:"header"`
	Cases  []json.RawMessage `json:"cases"`
}

// oracles are the sources a corpus file may name, in the order the README ranks them.
var oracles = []string{"production", "sdk", "docs", "emulator", "hidane"}

// generatorHash is the header.versions key holding the SHA-256 of the recorder that wrote a
// corpus file (production/lib.mjs, GENERATOR_HASH).
const generatorHash = "generator sha256"

// exceptionKinds are parity-exceptions.json's kinds (ADR-023 §3, ADR-028 §7.4).
var exceptionKinds = []string{"restriction", "provisional", "emulator-wrong"}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// repoRoot is the directory of go.mod above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the oracle")
		}
		dir = parent
	}
}

// TestManifestPinsCorpus: every manifest entry names a corpus file whose SHA-256 and header
// match, and every corpus file has an entry.
func TestManifestPinsCorpus(t *testing.T) {
	var m manifest
	readJSON(t, "manifest.json", &m)
	if m.Schema != "aruma.documents-oracle.manifest/v1" {
		t.Errorf("manifest schema %q", m.Schema)
	}
	for name, e := range m.Files {
		b, err := os.ReadFile(filepath.FromSlash(name))
		if err != nil {
			t.Errorf("%s: listed in manifest.json but %v", name, err)
			continue
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != e.SHA256 {
			t.Errorf("%s: sha256 %s, manifest.json pins %s; re-record it or rebuild the manifest (README.md)", name, got, e.SHA256)
		}
		var f corpusFile
		if err := json.Unmarshal(b, &f); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		h := f.Header
		if h.Oracle != e.Oracle || h.Generator != e.Generator || h.RecordedAt != e.RecordedAt || !reflect.DeepEqual(h.Versions, e.Versions) {
			t.Errorf("%s: header %+v and manifest entry %+v disagree", name, h, e)
		}
	}
	entries, err := os.ReadDir("corpus")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range entries {
		name := "corpus/" + d.Name()
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			t.Errorf("%s: the corpus holds only JSON files", name)
			continue
		}
		if _, ok := m.Files[name]; !ok {
			t.Errorf("%s: no manifest.json entry; record it with a recorder, which rebuilds the manifest", name)
		}
	}
}

// TestCorpusHeaders: every corpus file names its oracle, generator, date and versions, and its
// cases have unique ids.
func TestCorpusHeaders(t *testing.T) {
	root := repoRoot(t)
	files, err := filepath.Glob("corpus/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus files: %v", err)
	}
	for _, name := range files {
		var f corpusFile
		readJSON(t, name, &f)
		h := f.Header
		if f.Schema != "aruma.documents-oracle/v1" {
			t.Errorf("%s: schema %q", name, f.Schema)
		}
		if !slices.Contains(oracles, h.Oracle) || h.Generator == "" || h.RecordedAt == "" || len(h.Versions) == 0 {
			t.Errorf("%s: incomplete header %+v", name, h)
		}
		// A generator in the repository must exist and be the script that wrote the file: its
		// SHA-256 is in the header (lib.mjs, writeCorpus), so a recorder edited since its
		// recording fails here until it is run again. Imported recordings name their origin.
		if script, _, _ := strings.Cut(h.Generator, " "); strings.HasPrefix(script, "test/") {
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(script)))
			if err != nil {
				t.Errorf("%s: generator %s: %v", name, script, err)
			} else if sum := sha256.Sum256(src); h.Versions[generatorHash] != hex.EncodeToString(sum[:]) {
				t.Errorf("%s: header %q is %q, but %s hashes to %x; re-record it with the script as it is", name, generatorHash, h.Versions[generatorHash], script, sum)
			}
		}
		seen := map[string]bool{}
		for _, raw := range f.Cases {
			var c struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" || seen[c.ID] {
				t.Errorf("%s: case id %q missing or repeated (%v)", name, c.ID, err)
			}
			seen[c.ID] = true
		}
	}
}

type parityException struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	Summary    string       `json:"summary"`
	Production string       `json:"production"`
	Emulator   string       `json:"emulator"`
	Aruma      string       `json:"aruma"`
	Follows    string       `json:"follows"`
	Evidence   []string     `json:"evidence"`
	Test       string       `json:"test"`
	Planned    *plannedTest `json:"planned"`
}

// plannedTest is a test that a later phase writes: an exception names it instead of pointing
// at an unrelated test that exists today.
type plannedTest struct {
	Test  string `json:"test"`
	Phase string `json:"phase"`
}

// phaseList is phases.json: the phases of v0.4 and those already closed.
type phaseList struct {
	Schema string   `json:"schema"`
	Phases []string `json:"phases"`
	Closed []string `json:"closed"`
}

func readPhases(t *testing.T) phaseList {
	t.Helper()
	var ph phaseList
	readJSON(t, "phases.json", &ph)
	if ph.Schema != "aruma.documents-phases/v1" || len(ph.Phases) == 0 {
		t.Fatalf("phases.json: schema %q, %d phases", ph.Schema, len(ph.Phases))
	}
	for _, c := range ph.Closed {
		if !slices.Contains(ph.Phases, c) {
			t.Errorf("phases.json: closed phase %q is not one of %q", c, ph.Phases)
		}
	}
	return ph
}

// TestParityExceptionsNameTests: every exception says what production, the emulator and Aruma
// do, cites corpus files the manifest pins, and names a test function that exists, or a planned
// test with the phase that writes it. CI fails on an exception whose test was renamed or never
// written, and on a planned test whose phase phases.json marks closed.
func TestParityExceptionsNameTests(t *testing.T) {
	root := repoRoot(t)
	var p struct {
		Schema     string            `json:"schema"`
		Exceptions []parityException `json:"exceptions"`
	}
	readJSON(t, "parity-exceptions.json", &p)
	var m manifest
	readJSON(t, "manifest.json", &m)
	ph := readPhases(t)
	if p.Schema != "aruma.documents-parity/v1" {
		t.Errorf("parity-exceptions schema %q", p.Schema)
	}
	seen := map[string]bool{}
	for _, e := range p.Exceptions {
		if e.ID == "" || seen[e.ID] {
			t.Errorf("exception id %q missing or repeated", e.ID)
		}
		seen[e.ID] = true
		if e.Summary == "" || e.Production == "" || e.Emulator == "" || e.Aruma == "" {
			t.Errorf("%s: summary, production, emulator and aruma are all required", e.ID)
		}
		if !slices.Contains(exceptionKinds, e.Kind) {
			t.Errorf("%s: kind %q is not one of %q", e.ID, e.Kind, exceptionKinds)
		}
		// An entry cites the recording it rests on, or says that production was not recorded.
		if len(e.Evidence) == 0 && !strings.HasPrefix(e.Production, "Not recorded") {
			t.Errorf("%s: no evidence, and its production text does not start with \"Not recorded\"", e.ID)
		}
		// The plan's rule: where sources differ, Aruma follows production.
		if e.Follows != "production" {
			t.Errorf("%s: follows %q; Aruma follows production", e.ID, e.Follows)
		}
		for _, ev := range e.Evidence {
			if _, ok := m.Files[ev]; !ok {
				t.Errorf("%s: evidence %s is not a pinned corpus file", e.ID, ev)
			}
		}
		for _, err := range checkTests(root, ph, e) {
			t.Errorf("%s: %v", e.ID, err)
		}
	}
}

// checkTests checks the tests an exception names: test, which must exist now, and planned, which
// a phase will write. A planned test must name a phase that is still open and a test that does
// not exist yet: once written it moves to test, and once its phase is closed CI fails until it
// does.
func checkTests(root string, ph phaseList, e parityException) []error {
	if e.Test == "" && e.Planned == nil {
		return []error{errors.New("names neither a test nor a planned test")}
	}
	var errs []error
	if e.Test != "" {
		if err := testExists(root, e.Test); err != nil {
			errs = append(errs, err)
		}
	}
	p := e.Planned
	if p == nil {
		return errs
	}
	switch {
	case !slices.Contains(ph.Phases, p.Phase):
		errs = append(errs, fmt.Errorf("planned test %s: phase %q is not one of %q (phases.json)", p.Test, p.Phase, ph.Phases))
	case slices.Contains(ph.Closed, p.Phase):
		errs = append(errs, fmt.Errorf("planned test %s was due in %s, which phases.json marks closed: write it and name it in test", p.Test, p.Phase))
	}
	if _, _, err := splitTestRef(p.Test); err != nil {
		errs = append(errs, fmt.Errorf("planned %w", err))
	} else if testExists(root, p.Test) == nil {
		errs = append(errs, fmt.Errorf("planned test %s exists: move it from planned to test", p.Test))
	}
	return errs
}

// splitTestRef splits "path/x_test.go:TestName".
func splitTestRef(ref string) (file, fn string, err error) {
	file, fn, ok := strings.Cut(ref, ":")
	if !ok || !strings.HasSuffix(file, "_test.go") || !strings.HasPrefix(fn, "Test") {
		return "", "", fmt.Errorf("test %q is not path/to/x_test.go:TestName", ref)
	}
	return file, fn, nil
}

// testExists checks that "path/x_test.go:TestName" names a test in that file: a top-level
// func TestName(t *testing.T) with a body, found by parsing the file (so a test inside a comment
// does not count), that never calls Skip, Skipf or SkipNow.
func testExists(root, ref string) error {
	file, fn, err := splitTestRef(ref)
	if err != nil {
		return err
	}
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file)), nil, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("test %s: %w", ref, err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name.Name != fn {
			continue
		}
		if fd.Body == nil || !takesTestingT(fd.Type) {
			return fmt.Errorf("test %s: %s is not func %s(t *testing.T) { … }", ref, fn, fn)
		}
		skips := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && slices.Contains([]string{"Skip", "Skipf", "SkipNow"}, sel.Sel.Name) {
					skips = true
				}
			}
			return !skips
		})
		if skips {
			return fmt.Errorf("test %s: %s may skip, so it proves nothing", ref, fn)
		}
		return nil
	}
	return fmt.Errorf("test %s: no func %s(t *testing.T) in %s", ref, fn, file)
}

// takesTestingT reports whether a function type is func(t *testing.T) with no result.
func takesTestingT(ft *ast.FuncType) bool {
	if ft.TypeParams != nil || ft.Results != nil || len(ft.Params.List) != 1 || len(ft.Params.List[0].Names) > 1 {
		return false
	}
	star, ok := ft.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "T"
}

type indexField struct {
	FieldPath   string `json:"fieldPath"`
	Order       string `json:"order,omitempty"`
	ArrayConfig string `json:"arrayConfig,omitempty"`
}

type indexDef struct {
	Name       string       `json:"name"`
	QueryScope string       `json:"queryScope"`
	Fields     []indexField `json:"fields"`
}

type indexLink struct {
	Param   string   `json:"param"`
	Payload string   `json:"payload"`
	Index   indexDef `json:"index"`
}

type indexOutcome struct {
	Verdict  string `json:"verdict"`
	HTTP     int    `json:"http"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Template *struct {
		ID   string            `json:"id"`
		Text string            `json:"text"`
		Args map[string]string `json:"args"`
	} `json:"template"`
	Links      []indexLink `json:"links"`
	IDs        any         `json:"ids"`
	Aggregates any         `json:"aggregates"`
}

type indexCase struct {
	ID string `json:"id"`
	indexOutcome
	Empty indexOutcome `json:"emptyCollection"`
}

func loadIndexCases(t *testing.T, name string) []indexCase {
	t.Helper()
	var f struct {
		Cases []indexCase `json:"cases"`
	}
	readJSON(t, name, &f)
	return f.Cases
}

// TestIndexCorpus: the 105 production verdicts are ok or FAILED_PRECONDITION, each refusal's
// template reproduces its message byte for byte, its links are the URL's create_* parameters,
// each payload decodes to the recorded index, and the never-written collection got the same
// verdict as the populated one.
func TestIndexCorpus(t *testing.T) {
	cases := loadIndexCases(t, "corpus/indexes-105.json")
	refused := 0
	for _, c := range cases {
		for _, o := range []indexOutcome{c.indexOutcome, c.Empty} {
			switch o.Verdict {
			case "ok":
				continue
			case "FAILED_PRECONDITION":
			default:
				t.Errorf("%s: verdict %q", c.ID, o.Verdict)
				continue
			}
			if o.HTTP != 400 || o.Status != "FAILED_PRECONDITION" || o.Template == nil {
				t.Errorf("%s: refusal without HTTP 400, status or template", c.ID)
				continue
			}
			if got := render(o.Template.Text, o.Template.Args); got != o.Message {
				t.Errorf("%s: template renders %q, message is %q", c.ID, got, o.Message)
			}
			if err := checkLinks(o.Template.Args["url"], o.Links); err != nil {
				t.Errorf("%s: %v", c.ID, err)
			}
		}
		if c.Verdict != c.Empty.Verdict {
			t.Errorf("%s: %s on the populated collection, %s on the empty one", c.ID, c.Verdict, c.Empty.Verdict)
		}
		if c.Verdict != "ok" {
			refused++
		}
	}
	if len(cases) != 105 || refused != 45 {
		t.Errorf("%d shapes, %d refused; recorded 105 and 45", len(cases), refused)
	}
}

func render(text string, args map[string]string) string {
	for k, v := range args {
		text = strings.ReplaceAll(text, "{"+k+"}", v)
	}
	return text
}

func checkLinks(url string, links []indexLink) error {
	_, query, ok := strings.Cut(url, "?")
	if !ok {
		return fmt.Errorf("link %q has no query", url)
	}
	var params []string
	for kv := range strings.SplitSeq(query, "&") {
		if strings.HasPrefix(kv, "create_") {
			params = append(params, kv)
		}
	}
	if len(params) != len(links) || len(links) == 0 {
		return fmt.Errorf("%d create_* parameters, %d links", len(params), len(links))
	}
	for i, l := range links {
		if params[i] != l.Param+"="+l.Payload {
			return fmt.Errorf("link %d: %s is not %s=%s", i, params[i], l.Param, l.Payload)
		}
		got, err := decodeIndex(l.Payload)
		if err != nil {
			return fmt.Errorf("link %d: %w", i, err)
		}
		if !reflect.DeepEqual(got, l.Index) {
			return fmt.Errorf("link %d decodes to %+v, recorded %+v", i, got, l.Index)
		}
		if f := l.Index.Fields; l.Param == "create_composite" && f[len(f)-1].FieldPath != "__name__" {
			return fmt.Errorf("link %d: a composite that does not end with __name__", i)
		}
	}
	return nil
}

// decodeIndex reads a serialised google.firestore.admin.v1.Index from the unpadded base64 of a
// console link: 1 name, 2 query_scope, 3 fields (1 field_path, 2 order, 3 array_config).
func decodeIndex(payload string) (indexDef, error) {
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return indexDef{}, err
	}
	var idx indexDef
	scopes := map[uint64]string{1: "COLLECTION", 2: "COLLECTION_GROUP", 3: "COLLECTION_RECURSIVE"}
	orders := map[uint64]string{1: "ASCENDING", 2: "DESCENDING"}
	err = walk(b, func(num, n uint64, sub []byte) error {
		switch num {
		case 1:
			idx.Name = string(sub)
		case 2:
			idx.QueryScope = scopes[n]
		case 3:
			var f indexField
			if err := walk(sub, func(num, n uint64, s []byte) error {
				switch num {
				case 1:
					f.FieldPath = string(s)
				case 2:
					f.Order = orders[n]
				case 3:
					if n == 1 {
						f.ArrayConfig = "CONTAINS"
					}
				default:
					return fmt.Errorf("IndexField field %d", num)
				}
				return nil
			}); err != nil {
				return err
			}
			idx.Fields = append(idx.Fields, f)
		default:
			return fmt.Errorf("Index field %d", num)
		}
		return nil
	})
	return idx, err
}

// walk calls fn for each field of a protobuf message with varint (n) or bytes (sub) values.
func walk(b []byte, fn func(num, n uint64, sub []byte) error) error {
	for len(b) > 0 {
		key, k := uvarint(b)
		if k == 0 {
			return errors.New("bad key")
		}
		b = b[k:]
		var n uint64
		var sub []byte
		switch key & 7 {
		case 0:
			n, k = uvarint(b)
			if k == 0 {
				return errors.New("bad varint")
			}
			b = b[k:]
		case 2:
			l, k := uvarint(b)
			if k == 0 || uint64(len(b)-k) < l {
				return errors.New("bad length")
			}
			sub, b = b[k:k+int(l)], b[k+int(l):]
		default:
			return fmt.Errorf("wire type %d", key&7)
		}
		if err := fn(key>>3, n, sub); err != nil {
			return err
		}
	}
	return nil
}

func uvarint(b []byte) (uint64, int) {
	var x uint64
	for i, c := range b {
		if i == 10 {
			return 0, 0
		}
		x |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}

// TestIndexEnforcementEmulatorDiffers proves parity-exceptions.json "emulator-index-enforcement":
// production refuses 45 of the 105 shapes for a missing composite index; the emulator serves 43
// of them and refuses the other two (__name__ descending) with its own message; on the 60 shapes
// production serves, the emulator returns the same documents in the same order and the same
// aggregates.
func TestIndexEnforcementEmulatorDiffers(t *testing.T) {
	prod := loadIndexCases(t, "corpus/indexes-105.json")
	emu := map[string]indexCase{}
	for _, c := range loadIndexCases(t, "corpus/indexes-105.emulator.json") {
		emu[c.ID] = c
	}
	served := 0
	var keyScans []string
	for _, p := range prod {
		e, ok := emu[p.ID]
		if !ok {
			t.Errorf("%s: not run on the emulator", p.ID)
			continue
		}
		switch {
		case p.Verdict == "ok":
			if e.Verdict != "ok" || !reflect.DeepEqual(p.IDs, e.IDs) || !reflect.DeepEqual(p.Aggregates, e.Aggregates) {
				t.Errorf("%s: production and the emulator answer differently", p.ID)
			}
		case e.Verdict == "ok":
			served++
		case e.Message == "Firestore does not support descending key scans":
			keyScans = append(keyScans, p.ID)
		default:
			t.Errorf("%s: the emulator refuses with %q", p.ID, e.Message)
		}
	}
	slices.Sort(keyScans)
	if served != 43 || !slices.Equal(keyScans, []string{"cg_name_desc", "name_desc"}) {
		t.Errorf("the emulator serves %d production refusals (recorded 43) and refuses %v as key scans", served, keyScans)
	}
}

// TestTestExistsRefusesFakes: the ways a parity exception's test reference could pass without
// a real test are refused (a test inside a comment, one that skips, a method, a wrong
// signature), and a real test passes.
func TestTestExistsRefusesFakes(t *testing.T) {
	root := t.TempDir()
	src := "package x\n\nimport \"testing\"\n\n" +
		"/*\nfunc TestCommented(t *testing.T) {}\n*/\n\n" +
		"func TestSkips(t *testing.T) {\n\tif true {\n\t\tt.Skip(\"later\")\n\t}\n}\n\n" +
		"func TestSubtestSkips(t *testing.T) {\n\tt.Run(\"a\", func(t *testing.T) { t.Skipf(\"%d\", 1) })\n}\n\n" +
		"type s struct{}\n\nfunc (s) TestMethod(t *testing.T) {}\n\n" +
		"func TestBench(b *testing.B) {}\n\n" +
		"func TestReal(t *testing.T) {\n\tif 1+1 != 2 {\n\t\tt.Fatal(\"arithmetic\")\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(root, "x_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"TestCommented", "TestSkips", "TestSubtestSkips", "TestMethod", "TestBench", "TestMissing"} {
		if err := testExists(root, "x_test.go:"+fn); err == nil {
			t.Errorf("%s accepted", fn)
		}
	}
	if err := testExists(root, "x_test.go:TestReal"); err != nil {
		t.Errorf("TestReal: %v", err)
	}
}

// TestPlannedTestsChecked: a planned test is refused once its phase is closed, when its phase is
// unknown, when it already exists (it belongs in test then), and when it is not a test
// reference; an entry with no test at all is refused; an open phase is accepted.
func TestPlannedTestsChecked(t *testing.T) {
	root := t.TempDir()
	src := "package x\n\nimport \"testing\"\n\nfunc TestReal(t *testing.T) {\n\tif 1+1 != 2 {\n\t\tt.Fatal(\"arithmetic\")\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(root, "x_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ph := phaseList{Phases: []string{"F0", "F1", "F2"}, Closed: []string{"F0"}}
	planned := func(test, phase string) parityException {
		return parityException{Planned: &plannedTest{Test: test, Phase: phase}}
	}
	refused := map[string]parityException{
		"closed phase":    planned("x_test.go:TestLater", "F0"),
		"unknown phase":   planned("x_test.go:TestLater", "F7"),
		"already written": planned("x_test.go:TestReal", "F1"),
		"not a test":      planned("x.go:Later", "F1"),
		"no test at all":  {},
		"missing test":    {Test: "x_test.go:TestMissing"},
	}
	for name, e := range refused {
		if errs := checkTests(root, ph, e); len(errs) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	accepted := map[string]parityException{
		"open phase":            planned("x_test.go:TestLater", "F1"),
		"test and planned test": {Test: "x_test.go:TestReal", Planned: &plannedTest{Test: "y_test.go:TestLater", Phase: "F2"}},
	}
	for name, e := range accepted {
		if errs := checkTests(root, ph, e); len(errs) != 0 {
			t.Errorf("%s refused: %v", name, errs)
		}
	}
}
