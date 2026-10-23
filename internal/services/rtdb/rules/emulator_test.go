package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// The golden files in testdata/emulator hold verdicts captured from the official emulator
// 4.11.2 by capture.mjs (see its header for how to recapture). Each check ran over REST and
// over the protocol; the engine must reach the same verdict for both, except where a known
// difference below says otherwise and why.

type goldenFile struct {
	Provenance map[string]string `json:"provenance"`
	Loads      []struct {
		Name   string `json:"name"`
		Doc    string `json:"doc"`
		Status int    `json:"status"`
		Error  string `json:"error"`
	} `json:"loads"`
	Groups []goldenGroup `json:"groups"`
}

type goldenGroup struct {
	Name      string          `json:"name"`
	Rules     json.RawMessage `json:"rules"`
	RulesText *string         `json:"rulesText"`
	Data      any             `json:"data"`
	LoadError string          `json:"loadError"`
	Checks    []goldenCheck   `json:"checks"`
}

type goldenCheck struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
	Query *struct {
		OrderBy      string `json:"orderBy"`
		StartAt      any    `json:"startAt"`
		EndAt        any    `json:"endAt"`
		EqualTo      any    `json:"equalTo"`
		LimitToFirst int    `json:"limitToFirst"`
		LimitToLast  int    `json:"limitToLast"`
	} `json:"query"`
	Auth *struct {
		UID   string         `json:"uid"`
		Token map[string]any `json:"token"`
	} `json:"auth"`
	Rest *string `json:"rest"`
	WS   string  `json:"ws"`
}

// knownDifferences are the verdicts where Aruma deliberately departs from the emulator, keyed
// by group name, or by "group name #check transport" for one verdict.
var knownDifferences = map[string]string{
	// The reference lists \s and \S; the emulator refuses them ("'whitespacechar' not found").
	"expr: 'a b'.matches(/a\\sb/)": "regex: the reference wins over the emulator for \\s",
	"expr: 'a1b'.matches(/a\\Sb/)": "regex: the reference wins over the emulator for \\S",
}

func readGolden(t *testing.T, name string) goldenFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/emulator/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if g.Provenance["jar"] == "" || g.Provenance["captured"] == "" {
		t.Fatalf("%s has no provenance", name)
	}
	return g
}

func splitPath(p string) []string {
	var out []string
	for _, k := range strings.Split(p, "/") {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

// query builds the query the check asked for, over the given transport.
func (c goldenCheck) query(transport string) Query {
	if c.Query == nil {
		if transport == "rest" {
			return PlainREST
		}
		return PlainListen
	}
	q := Query{StartAt: c.Query.StartAt, EndAt: c.Query.EndAt, EqualTo: c.Query.EqualTo,
		LimitToFirst: c.Query.LimitToFirst, LimitToLast: c.Query.LimitToLast}
	switch c.Query.OrderBy {
	case "key":
		q.OrderByKey = true
	case "value":
		q.OrderByValue = true
	case "priority", "":
		q.OrderByPriority = true
	default:
		q.OrderByChild = c.Query.OrderBy
	}
	return q
}

// verdict asks the engine about one check over one transport.
func verdict(rs *Ruleset, g goldenGroup, c goldenCheck, transport string) (string, error) {
	data, err := NewMemorySource(g.Data)
	if err != nil {
		return "", err
	}
	env := Env{Auth: Unauthenticated(), Now: time.Now(), Data: data}
	if c.Auth != nil {
		if env.Auth, err = User(c.Auth.UID, c.Auth.Token); err != nil {
			return "", err
		}
	}
	ctx := context.Background()
	path := splitPath(c.Path)
	var ok bool
	switch c.Op {
	case "read":
		ok, err = rs.CanRead(ctx, env, path, c.query(transport))
	case "set", "onDisconnectSet":
		ok, err = rs.CanWrite(ctx, env, Change{Path: path, Value: c.Value})
	case "delete":
		ok, err = rs.CanWrite(ctx, env, Change{Path: path})
	case "update":
		ok, err = rs.CanWrite(ctx, env, Update(path, c.Value.(map[string]any))...)
	case "onDisconnectUpdate":
		// The emulator checks an onDisconnect update one child at a time, each against the
		// stored data, unlike an update, whose children are checked together.
		ok = true
		for _, ch := range Update(path, c.Value.(map[string]any)) {
			if ok, err = rs.CanWrite(ctx, env, ch); !ok || err != nil {
				break
			}
		}
	case "onDisconnectCancel":
		// Cancelling checks no rule: the emulator allows it even where writes are closed.
		ok = true
	default:
		return "", fmt.Errorf("unknown op %s", c.Op)
	}
	if err != nil {
		return "", err
	}
	if ok {
		return "allow", nil
	}
	return "deny", nil
}

type agreement struct{ agree, differ, known int }

// replayGolden runs every group of a golden file and fails on any difference that is not known.
func replayGolden(t *testing.T, gf goldenFile, known map[string]string) agreement {
	var a agreement
	used := map[string]bool{}
	seen := map[string]bool{}
	for _, g := range gf.Groups {
		if seen[g.Name] {
			t.Fatalf("two groups named %q", g.Name)
		}
		seen[g.Name] = true
		doc := []byte(g.Rules)
		if g.RulesText != nil {
			doc = []byte(*g.RulesText)
		}
		rs, perr := Parse(doc)
		if why, ok := known[g.Name]; ok {
			used[g.Name] = true
			t.Logf("known difference, %s: %s", g.Name, why)
			a.known += max(1, 2*len(g.Checks))
			continue
		}
		if g.LoadError != "" {
			if perr == nil {
				t.Errorf("%s: loads, the emulator refused it: %s", g.Name, g.LoadError)
				a.differ++
			} else {
				a.agree++
			}
			continue
		}
		if perr != nil {
			t.Errorf("%s: %v", g.Name, perr)
			a.differ += 2 * len(g.Checks)
			continue
		}
		for i, c := range g.Checks {
			for _, tr := range []struct {
				name string
				want *string
			}{{"rest", c.Rest}, {"ws", &c.WS}} {
				if tr.want == nil || strings.HasPrefix(*tr.want, "error") {
					continue // not a rules decision, such as REST's 400 for a missing index
				}
				got, err := verdict(rs, g, c, tr.name)
				if err != nil {
					t.Fatalf("%s #%d: %v", g.Name, i, err)
				}
				key := fmt.Sprintf("%s #%d %s", g.Name, i, tr.name)
				if got == *tr.want {
					a.agree++
					continue
				}
				if why, ok := known[key]; ok {
					used[key] = true
					a.known++
					t.Logf("known difference, %s: %s", key, why)
					continue
				}
				a.differ++
				t.Errorf("%s: %s %s, engine says %s, emulator %s", key, c.Op, c.Path, got, *tr.want)
			}
		}
	}
	var stale []string
	for k := range known {
		if group, _, _ := strings.Cut(k, " #"); seen[group] && !used[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		t.Errorf("known difference %q no longer differs", k)
	}
	return a
}

func TestEmulatorVerdicts(t *testing.T) {
	gf := readGolden(t, "matrix.golden.json")
	a := replayGolden(t, gf, knownDifferences)
	t.Logf("emulator matrix: %d verdicts agree, %d differ, %d known differences (%.1f%% agreement)",
		a.agree, a.differ, a.known, 100*float64(a.agree)/float64(a.agree+a.differ+a.known))
}

// TestEmulatorApps replays the rules files of twenty app shapes (testdata/apps) with the
// operations their clients send.
func TestEmulatorApps(t *testing.T) {
	gf := readGolden(t, "apps.golden.json")
	if len(gf.Groups) != 20 {
		t.Fatalf("%d apps, want 20", len(gf.Groups))
	}
	for _, g := range gf.Groups {
		disk, err := os.ReadFile("testdata/apps/" + g.Name)
		if err != nil {
			t.Fatal(err)
		}
		if g.RulesText == nil || string(disk) != *g.RulesText {
			t.Fatalf("%s changed since apps.golden.json was captured", g.Name)
		}
	}
	a := replayGolden(t, gf, nil)
	t.Logf("emulator apps: %d verdicts agree, %d differ (%.1f%% agreement)",
		a.agree, a.differ, 100*float64(a.agree)/float64(a.agree+a.differ))
}

// TestEmulatorNulls replays the writes that leave validated nodes null (nulls.mjs): .validate
// is skipped where the new data is null and runs everywhere else the write reaches. Lark
// 0.3.0 fixed both directions in its engine (a null in a multi-path update denied, the
// removal of a required child allowed); Aruma agreed with the emulator on every case here.
func TestEmulatorNulls(t *testing.T) {
	gf := readGolden(t, "nulls.golden.json")
	a := replayGolden(t, gf, nil)
	if a.agree < 100 {
		t.Fatalf("only %d verdicts replayed", a.agree)
	}
	t.Logf("emulator nulls: %d verdicts agree, %d differ", a.agree, a.differ)
}

// knownLoadDifferences are rules documents Aruma loads differently from the emulator.
var knownLoadDifferences = map[string]string{
	".read: data.getPriority() == null":          "getPriority() is refused (decision Q4)",
	".read: auth.token.email.matches(/a\\sb/)":   "\\s is in the reference; the emulator refuses it",
	".read: auth.token.email.matches(/a||b/)":    "an empty alternative is refused; the emulator loads it and never matches",
	".read: auth.token.email.matches(/a{2000}/)": "Go's regexp repeats at most 1000 times",
	".read: auth.token.email.matches(/[z-a]/)":   "a reversed range is refused; the emulator loads it",
}

func TestEmulatorLoads(t *testing.T) {
	gf := readGolden(t, "matrix.golden.json")
	agree, positions, refused := 0, 0, 0
	for _, l := range gf.Loads {
		if l.Status != 200 {
			refused++
		}
		_, err := Parse([]byte(l.Doc))
		ok := err == nil
		want := l.Status == 200
		if ok == want {
			agree++
			if !ok {
				positions++
				if !samePosition(t, l.Name, err, l.Error) {
					positions--
				}
			}
			if why, known := knownLoadDifferences[l.Name]; known {
				t.Errorf("%s: no longer differs (%s)", l.Name, why)
			}
			continue
		}
		if why, known := knownLoadDifferences[l.Name]; known {
			t.Logf("known difference, %s: %s", l.Name, why)
			continue
		}
		if want {
			t.Errorf("%s: refused, the emulator loads it: %v", l.Name, err)
		} else {
			t.Errorf("%s: loads, the emulator refuses it: %s", l.Name, l.Error)
		}
	}
	t.Logf("emulator loads: %d of %d agree, %d known differences; %d of %d refusals point at the same line and column",
		agree, len(gf.Loads), len(knownLoadDifferences), positions, refused)
}

// samePosition reports whether the first error points where the emulator's first error does.
func samePosition(t *testing.T, name string, err error, emu string) bool {
	t.Helper()
	var first *Error
	switch e := err.(type) {
	case *Error:
		first = e
	case Errors:
		first = e[0]
	}
	var line, col int
	if _, serr := fmt.Sscanf(emu, "%d:%d:", &line, &col); serr != nil {
		t.Errorf("%s: emulator error without a position: %s", name, emu)
		return false
	}
	if first.Line != line || first.Column != col {
		t.Logf("position differs, %s: engine %d:%d %s; emulator %s", name, first.Line, first.Column, first.Msg, emu)
		return false
	}
	return true
}
