// Package rulestest reads the rules oracle (test/oracle/rules, built by S0-A) into one Go type,
// binds each case to the engine's public contract, and tallies how far the engine agrees.
//
// It is for tests and tools only, by convention (ADR-021 §6, docs/research/rules-layout.md):
// the services never import it.
//
// The oracle has three provenances, in order of authority: deployed (real rules, real SDK
// requests), rules-test-api (the Rules API's projects.test: the language's semantics only) and
// emulator (the official emulators as black boxes). Its files come in two shapes, both described
// in test/oracle/rules/README.md:
//
//   - aruma.rules-oracle/v1 case files: production/<topic>.json and imported/*/normalised/*.json,
//     one self-contained case each;
//   - emulator/<topic>.json: what the emulator decided for each case of production/<topic>.json,
//     with the provenance at the file level.
//
// The held-out set lives on another branch, under test/oracle/rules/heldout/. Load never enters
// a directory named heldout, nor accepts a file whose set is heldout: no case of it can reach a
// tuning loop or a fuzz seed by accident (fixtures_test.go proves it).
package rulestest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// Schema is the oracle format this reader understands.
const Schema = "aruma.rules-oracle/v1"

// Provenance is what a case was recorded against (ADR-021 §6).
type Provenance string

const (
	Deployed Provenance = "deployed"       // real rules and real requests: decides everything
	TestAPI  Provenance = "rules-test-api" // projects.test: decides the language's semantics only
	Emulator Provenance = "emulator"       // the official emulator, as a black box
)

// Rank orders provenances by authority: deployed wins over the Test API, which wins over the
// emulator in the language's semantics.
func (p Provenance) Rank() int {
	switch p {
	case Deployed:
		return 3
	case TestAPI:
		return 2
	case Emulator:
		return 1
	}
	return 0
}

// Outcome is what the oracle recorded for a case.
type Outcome string

const (
	Allow Outcome = "allow"
	Deny  Outcome = "deny"  // a condition was false, or no statement matched
	Error Outcome = "error" // a denial caused by an error: evaluation, compile or request
	Fact  Outcome = "fact"  // not a decision: a value observed on deployed Storage (Pyric)
)

// Expected is a case's recorded result.
type Expected struct {
	Outcome   Outcome
	Reason    string // for deny: "false" or "no-matching-statement", when recorded
	ErrorKind string // for error: "evaluation", "compile" or "request"
	Message   string // as the source wrote it; for reading, not for the verdict
}

// Allows reports whether the case was allowed.
func (e Expected) Allows() bool { return e.Outcome == Allow }

// Compile reports whether the expected result is that the rules do not compile.
func (e Expected) Compile() bool { return e.Outcome == Error && e.ErrorKind == "compile" }

// SourceTooLarge reports whether the Test API refused the request because the rules file is too
// large: a compile-time rejection by another name (A-oracle-findings §1).
func (e Expected) SourceTooLarge() bool {
	return e.Outcome == Error && e.ErrorKind == "request" && strings.Contains(e.Message, "Total size of sources")
}

// compileLine is one line of a compile message: "ERROR L5:35 description" from the Test API,
// "L5:25 description" after "Error compiling rules:" from the emulator.
var compileLine = regexp.MustCompile(`^(?:ERROR )?L(\d+):(\d+) (.*)$`)

// FirstError returns the position and description of the first error in a compile message.
// Warnings, which some messages carry before the errors, are skipped.
func (e Expected) FirstError() (line, col int, desc string, ok bool) {
	for _, l := range strings.Split(e.Message, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "WARNING") {
			continue
		}
		m := compileLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		line, _ = strconv.Atoi(m[1])
		col, _ = strconv.Atoi(m[2])
		if line == 0 && col == 0 {
			// The emulator writes no severity, and puts the whole file's warning, the old
			// version's, at L0:0; production gives it no position (aruma/limits/let_rules_v1).
			continue
		}
		return line, col, m[3], true
	}
	return 0, 0, "", false
}

// Field is one field of a recorded request, resource or mock result: absent, null, or a JSON
// value. The Test API tells absent and null apart: a field it is not sent is undefined in the
// rules ("Property auth is undefined on object."), while null is a value.
type Field struct {
	raw json.RawMessage // nil when absent
}

// UnmarshalJSON keeps the field's JSON, null included.
func (f *Field) UnmarshalJSON(b []byte) error {
	f.raw = append(json.RawMessage{}, b...)
	return nil
}

// MarshalJSON writes the field's JSON; an absent field writes null, so omit it instead.
func (f Field) MarshalJSON() ([]byte, error) {
	if f.raw == nil {
		return []byte("null"), nil
	}
	return f.raw, nil
}

// FieldOf returns a present field holding v's JSON.
func FieldOf(v any) Field {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return Field{raw: b}
}

// Present reports whether the field was recorded at all.
func (f Field) Present() bool { return f.raw != nil }

// IsNull reports whether the field is present and null.
func (f Field) IsNull() bool { return f.raw != nil && strings.TrimSpace(string(f.raw)) == "null" }

// Raw returns the field's JSON, nil when absent.
func (f Field) Raw() json.RawMessage { return f.raw }

// Request is a recorded request, in the Test API's shape.
type Request struct {
	Method   string // get, list, create, update, delete; the Test API also takes write
	Path     string // /databases/(default)/documents/... or /b/<bucket>/o/...
	Auth     Field  // request.auth
	Time     Field  // request.time, RFC 3339
	Resource Field  // request.resource
	Query    Field  // request.query
}

// Mock is a functionMocks entry: the result a lookup of one document gives.
type Mock struct {
	Function string // get, exists, getAfter, existsAfter; firestore.get, firestore.exists in Storage
	Path     string // the document's path, /databases/(default)/documents/...
	Result   Field  // the result's value; absent when the mock's result is undefined (the call fails)
}

// Encoding names how a case's values are written.
const (
	EncodingJSON          = ""               // plain JSON, as the Test API reads it
	EncodingFirestoreREST = "firestore-rest" // Firestore REST typed values (hidane's emulator results)
)

// Case is one oracle case, from any of its sources.
type Case struct {
	ID          string
	File        string // the oracle file, relative to the root
	Category    string // the topic, or the case's group in an imported set
	Service     rules.Service
	Source      string // who recorded it: aruma, hidane or pyric
	Provenance  Provenance
	RecordedAt  string
	Version     string
	Description string
	Expression  string // the condition, when the case is one condition

	Ruleset   string // the rules file; empty when NoRuleset says why
	NoRuleset string

	Request   *Request // nil when NoRequest says why
	NoRequest string
	Resource  Field // the existing resource
	Mocks     []Mock

	// RawNumbers maps a placeholder number of the case to the text the request carried
	// instead (2.0, which JSON.stringify cannot write).
	RawNumbers map[string]string
	Encoding   string // EncodingJSON or EncodingFirestoreREST

	Expected Expected
	// Higher is what a source of higher authority recorded for the same case, when the case is
	// a comparison (emulator/<topic>.json): the Test API's result.
	Higher *Expected
	// HigherExcluded is why the higher source's case does not count, when it does not: its
	// verdict is then an artefact (no lookup limit, debug() missing), and allowing what the
	// case's own source denied is permissive whatever it says.
	HigherExcluded string

	Skip     string // why the source did not run the case (emulator/<topic>.json)
	Note     string
	Warnings string
}

// FileName returns the name the case's rules file had: what positions and messages name.
func (c *Case) FileName() string {
	if c.Service == rules.Storage {
		return "storage.rules"
	}
	return "firestore.rules"
}

// File describes one oracle file Load read.
type File struct {
	Path       string // relative to the root
	Schema     string
	Shape      string // "cases" or "comparison"
	Set        string
	Source     string
	Provenance Provenance // empty when it varies by case
	RecordedAt string
	Version    string
	SHA256     string // of the file's bytes
	Cases      int
}

// Set is the oracle as Load read it.
type Set struct {
	Root  string
	Files []File
	Cases []*Case
}

// skipDirs are the directories Load never enters. heldout is the held-out set (ADR-021 §6);
// the others hold no case files: raw API calls, upstream originals, the recorders.
var skipDirs = map[string]bool{"heldout": true, "raw": true, "original": true, "tools": true, "node_modules": true}

// ErrHeldOut is what Load answers for a file that declares itself part of the held-out set.
var ErrHeldOut = errors.New("rulestest: a held-out file is outside the development oracle")

// Load reads every case file under root: production/, emulator/ and imported/*/normalised/.
// It never enters a directory named heldout.
func Load(root string) (*Set, error) { return load(root, false) }

// LoadHeldOut reads the held-out set alone, from a directory outside the repository where its
// branch is checked out (test/oracle/rules/heldout/ there): every file must declare itself
// held out. It is for the one measurement of ADR-021 §6; the tests call it only when
// ARUMA_RULES_HELDOUT names that directory (heldout_test.go).
func LoadHeldOut(root string) (*Set, error) { return load(root, true) }

func load(root string, heldOut bool) (*Set, error) {
	set := &Set{Root: root}
	var caseFiles, comparisonFiles []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] && !(heldOut && d.Name() == "heldout") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "emulator" {
			comparisonFiles = append(comparisonFiles, path)
		} else {
			caseFiles = append(caseFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(caseFiles)
	sort.Strings(comparisonFiles)
	byID := map[string]*Case{}
	for _, p := range caseFiles {
		cs, f, err := readCaseFile(root, p, heldOut)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		set.Files = append(set.Files, *f)
		for _, c := range cs {
			if byID[c.ID] != nil {
				return nil, fmt.Errorf("rulestest: %s: case %s appears twice", f.Path, c.ID)
			}
			byID[c.ID] = c
			set.Cases = append(set.Cases, c)
		}
	}
	for _, p := range comparisonFiles {
		cs, f, err := readComparisonFile(root, p, byID, heldOut)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		set.Files = append(set.Files, *f)
		set.Cases = append(set.Cases, cs...)
	}
	return set, nil
}

// fileHeader is what both shapes carry at the file level.
type fileHeader struct {
	Schema     string                     `json:"schema"`
	Topic      string                     `json:"topic"`
	Service    string                     `json:"service"`
	Set        string                     `json:"set"`
	Source     string                     `json:"source"`
	Provenance Provenance                 `json:"provenance"`
	RecordedAt string                     `json:"recordedAt"`
	Version    string                     `json:"version"`
	Rulesets   map[string]string          `json:"rulesets"`
	Resources  map[string]json.RawMessage `json:"resources"`
}

type rawExpected struct {
	Outcome   Outcome `json:"outcome"`
	Reason    string  `json:"reason"`
	ErrorKind string  `json:"errorKind"`
	Message   string  `json:"message"`
	Skip      string  `json:"skip"`
}

func (r rawExpected) expected() Expected {
	return Expected{Outcome: r.Outcome, Reason: r.Reason, ErrorKind: r.ErrorKind, Message: r.Message}
}

type rawCase struct {
	ID               string       `json:"id"`
	Service          string       `json:"service"`
	Source           string       `json:"source"`
	Provenance       Provenance   `json:"provenance"`
	RecordedAt       string       `json:"recordedAt"`
	Version          string       `json:"version"`
	Description      string       `json:"description"`
	Expression       string       `json:"expression"`
	Ruleset          *string      `json:"ruleset"`
	RulesetRef       string       `json:"rulesetRef"`
	RulesetGenerated *Generator   `json:"rulesetGenerated"`
	RulesetNote      string       `json:"rulesetNote"`
	Request          *rawRequest  `json:"request"`
	Resource         Field        `json:"resource"`
	ResourceRef      string       `json:"resourceRef"`
	FunctionMocks    []rawMock    `json:"functionMocks"`
	ValueEncoding    string       `json:"valueEncoding"`
	RawNumbers       []rawNumber  `json:"rawNumbers"`
	Expected         *rawExpected `json:"expected"`
	Note             string       `json:"note"`
	Warnings         string       `json:"warnings"`
}

type rawRequest struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Auth     Field  `json:"auth"`
	Time     Field  `json:"time"`
	Resource Field  `json:"resource"`
	Query    Field  `json:"query"`
}

type rawMock struct {
	Function string `json:"function"`
	Args     []struct {
		ExactValue *string `json:"exactValue"`
		AnyValue   *struct {
		} `json:"anyValue"`
	} `json:"args"`
	Result struct {
		Value     Field            `json:"value"`
		Undefined *json.RawMessage `json:"undefined"`
	} `json:"result"`
}

type rawNumber struct {
	From json.Number `json:"from"`
	To   string      `json:"to"`
}

func readJSON(root, path string, v any) (rel, sum string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	rel, _ = filepath.Rel(root, path)
	rel = filepath.ToSlash(rel)
	if err := json.Unmarshal(b, v); err != nil {
		return "", "", fmt.Errorf("rulestest: %s: %w", rel, err)
	}
	h := sha256.Sum256(b)
	return rel, hex.EncodeToString(h[:]), nil
}

// ErrNotHeldOut is what LoadHeldOut answers for a file that does not declare itself held out.
var ErrNotHeldOut = errors.New("rulestest: a development file is outside the held-out set")

// checkHeader rejects a held-out file (or, reading the held-out set, any other) and reports
// whether the file is an oracle file at all.
func checkHeader(rel string, h *fileHeader, heldOut bool) (bool, error) {
	if h.Schema != Schema {
		return false, nil
	}
	isHeldOut := strings.EqualFold(h.Set, "heldout") || strings.EqualFold(h.Set, "held-out")
	switch {
	case isHeldOut && !heldOut:
		return false, fmt.Errorf("%w: %s", ErrHeldOut, rel)
	case !isHeldOut && heldOut:
		return false, fmt.Errorf("%w: %s", ErrNotHeldOut, rel)
	}
	return true, nil
}

func service(s string) (rules.Service, error) {
	switch s {
	case "firestore":
		return rules.Firestore, nil
	case "storage":
		return rules.Storage, nil
	}
	return "", fmt.Errorf("unknown service %q", s)
}

// category is the case's group in the report: the topic of our files, and the second part of an
// imported ID (hidane/<group>/<n>, pyric/<service>/<scenario>/...).
func category(topic, id string) string {
	if topic != "" {
		return topic
	}
	parts := strings.Split(id, "/")
	switch {
	case parts[0] == "pyric" && len(parts) > 2:
		return parts[2]
	case len(parts) > 1:
		return parts[1]
	}
	return id
}

func readCaseFile(root, path string, heldOut bool) ([]*Case, *File, error) {
	var doc struct {
		fileHeader
		Cases []rawCase `json:"cases"`
	}
	rel, sum, err := readJSON(root, path, &doc)
	if err != nil {
		return nil, nil, err
	}
	if ok, err := checkHeader(rel, &doc.fileHeader, heldOut); !ok || err != nil {
		return nil, nil, err
	}
	f := &File{Path: rel, Schema: doc.Schema, Shape: "cases", Set: doc.Set, Source: doc.Source,
		Provenance: doc.Provenance, RecordedAt: doc.RecordedAt, Version: doc.Version, SHA256: sum, Cases: len(doc.Cases)}
	cases := make([]*Case, 0, len(doc.Cases))
	for i := range doc.Cases {
		c, err := doc.fileHeader.caseOf(rel, &doc.Cases[i])
		if err != nil {
			return nil, nil, fmt.Errorf("rulestest: %s: case %d (%s): %w", rel, i, doc.Cases[i].ID, err)
		}
		cases = append(cases, c)
	}
	return cases, f, nil
}

func (h *fileHeader) caseOf(rel string, r *rawCase) (*Case, error) {
	if r.ID == "" {
		return nil, errors.New("no id")
	}
	svcName := r.Service
	if svcName == "" {
		svcName = h.Service
	}
	svc, err := service(svcName)
	if err != nil {
		return nil, err
	}
	c := &Case{
		ID:          r.ID,
		File:        rel,
		Category:    category(h.Topic, r.ID),
		Service:     svc,
		Source:      first(r.Source, h.Source),
		Provenance:  Provenance(first(string(r.Provenance), string(h.Provenance))),
		RecordedAt:  first(r.RecordedAt, h.RecordedAt),
		Version:     first(r.Version, h.Version),
		Description: r.Description,
		Expression:  r.Expression,
		Encoding:    r.ValueEncoding,
		Note:        r.Note,
		Warnings:    r.Warnings,
	}
	if c.Provenance.Rank() == 0 {
		return nil, fmt.Errorf("unknown provenance %q", c.Provenance)
	}
	if r.Expected == nil {
		return nil, errors.New("no expected result")
	}
	c.Expected = r.Expected.expected()
	switch c.Encoding {
	case EncodingJSON, EncodingFirestoreREST:
	default:
		return nil, fmt.Errorf("unknown value encoding %q", c.Encoding)
	}

	switch {
	case r.Ruleset != nil:
		c.Ruleset = *r.Ruleset
	case r.RulesetRef != "":
		src, ok := h.Rulesets[r.RulesetRef]
		if !ok {
			return nil, fmt.Errorf("no ruleset %q", r.RulesetRef)
		}
		// hidane's rulesets are templates of one condition.
		c.Ruleset = strings.ReplaceAll(src, "{{expr}}", r.Expression)
	case r.RulesetGenerated != nil:
		src, err := r.RulesetGenerated.Generate()
		if err != nil {
			return nil, err
		}
		c.Ruleset = src
	default:
		c.NoRuleset = first(r.RulesetNote, "no ruleset was recorded")
	}

	if r.Request != nil {
		c.Request = &Request{Method: r.Request.Method, Path: r.Request.Path, Auth: r.Request.Auth,
			Time: r.Request.Time, Resource: r.Request.Resource, Query: r.Request.Query}
	} else {
		c.NoRequest = "no request was recorded"
		if c.Provenance == Deployed {
			c.NoRequest = "a real SDK request on deployed Storage: no request to replay (S2 binds Storage)"
		}
	}
	switch {
	case r.Resource.Present():
		c.Resource = r.Resource
	case r.ResourceRef != "":
		res, ok := h.Resources[r.ResourceRef]
		if !ok {
			return nil, fmt.Errorf("no resource %q", r.ResourceRef)
		}
		c.Resource = Field{raw: res}
	}
	for _, m := range r.FunctionMocks {
		mock, err := m.mock()
		if err != nil {
			return nil, err
		}
		c.Mocks = append(c.Mocks, mock)
	}
	if len(r.RawNumbers) > 0 {
		c.RawNumbers = map[string]string{}
		for _, n := range r.RawNumbers {
			c.RawNumbers[n.From.String()] = n.To
		}
	}
	return c, nil
}

func (m rawMock) mock() (Mock, error) {
	if len(m.Args) != 1 || m.Args[0].ExactValue == nil {
		return Mock{}, fmt.Errorf("mock of %s: only one exactValue argument is supported", m.Function)
	}
	out := Mock{Function: m.Function, Path: *m.Args[0].ExactValue}
	if m.Result.Undefined == nil {
		out.Result = m.Result.Value
		if !out.Result.Present() {
			return Mock{}, fmt.Errorf("mock of %s %s: no result", m.Function, out.Path)
		}
	}
	return out, nil
}

// readComparisonFile reads emulator/<topic>.json: for each case of production/<topic>.json, what
// the emulator decided. A comparison case is the production case with the emulator's result as
// its expected result, the Test API's as Higher, and the file's provenance.
func readComparisonFile(root, path string, byID map[string]*Case, heldOut bool) ([]*Case, *File, error) {
	var doc struct {
		fileHeader
		Cases []struct {
			ID         string       `json:"id"`
			Production *rawExpected `json:"production"`
			Emulator   *rawExpected `json:"emulator"`
		} `json:"cases"`
	}
	rel, sum, err := readJSON(root, path, &doc)
	if err != nil {
		return nil, nil, err
	}
	if ok, err := checkHeader(rel, &doc.fileHeader, heldOut); !ok || err != nil {
		return nil, nil, err
	}
	if doc.Provenance.Rank() == 0 {
		return nil, nil, fmt.Errorf("rulestest: %s: unknown provenance %q", rel, doc.Provenance)
	}
	f := &File{Path: rel, Schema: doc.Schema, Shape: "comparison", Set: doc.Set, Source: doc.Source,
		Provenance: doc.Provenance, RecordedAt: doc.RecordedAt, Version: doc.Version, SHA256: sum, Cases: len(doc.Cases)}
	cases := make([]*Case, 0, len(doc.Cases))
	for _, rc := range doc.Cases {
		base := byID[rc.ID]
		if base == nil {
			return nil, nil, fmt.Errorf("rulestest: %s: case %s is not in the case files", rel, rc.ID)
		}
		if rc.Emulator == nil {
			return nil, nil, fmt.Errorf("rulestest: %s: case %s has no result", rel, rc.ID)
		}
		c := *base
		c.File = rel
		c.Source = first(doc.Source, base.Source)
		c.Provenance = doc.Provenance
		c.RecordedAt = doc.RecordedAt
		c.Version = doc.Version
		c.Skip = rc.Emulator.Skip
		c.Expected = rc.Emulator.expected()
		higher := base.Expected
		if rc.Production != nil {
			higher = rc.Production.expected()
		}
		c.Higher = &higher
		c.HigherExcluded = Exclusion(base)
		cases = append(cases, &c)
	}
	return cases, f, nil
}

func first(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// Generator is a rulesetGenerated description: the size cases keep it instead of hundreds of
// kilobytes of rules. Generate is the Go form of generate in test/oracle/rules/tools/oracle.mjs.
type Generator struct {
	Kind  string `json:"kind"`
	Total int    `json:"total"` // comment-padding: the file's bytes in all
	Count int    `json:"count"` // functions: how many functions
}

// Generate returns the ruleset the generator describes.
func (g Generator) Generate() (string, error) {
	base := func(fns, cond string) string {
		return "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n" +
			fns + "\n    match /lim/{doc} {\n      allow get: if " + cond + ";\n    }\n  }\n}\n"
	}
	switch g.Kind {
	case "comment-padding":
		b := base("", "true")
		filler := g.Total - len(b) - 1
		if filler < 4 {
			return "", fmt.Errorf("comment-padding of %d bytes is too small", g.Total)
		}
		return "/*" + strings.Repeat("x", filler-4) + "*/\n" + b, nil
	case "functions":
		fns := make([]string, g.Count)
		for i := range fns {
			fns[i] = fmt.Sprintf("    function h%d(a) { return a == %d || a == 'v%d' || a in [%d, %d, %d]; }", i, i, i, i, i+1, i+2)
		}
		return base(strings.Join(fns, "\n"), "h0(0)"), nil
	}
	return "", fmt.Errorf("unknown ruleset generator %q", g.Kind)
}

// Root returns test/oracle/rules of the module that holds the working directory.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "test", "oracle", "rules"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("rulestest: no go.mod above the working directory")
		}
		dir = parent
	}
}
