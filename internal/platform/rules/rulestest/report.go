package rulestest

import (
	"fmt"
	"sort"
	"strings"
)

// Report tallies results by provenance and source, in a form pasted into
// docs/reference/compatibility.md.
type Report struct {
	Results []Result
}

// Add records one result.
func (r *Report) Add(res Result) { r.Results = append(r.Results, res) }

// Tally is the count of one group of results.
type Tally struct {
	Cases      int // all the group's cases
	Excluded   int // run but not counted (Exclusion, or not expressible)
	Counted    int
	Agree      int
	Finer      int // of the counted, those whose finer result agrees too
	Permissive int // of the counted, allows the source and every source above it denied
}

// Ratio is Agree over Counted, in percent; 0 when nothing counts.
func (t Tally) Ratio() float64 {
	if t.Counted == 0 {
		return 0
	}
	return 100 * float64(t.Agree) / float64(t.Counted)
}

func (t *Tally) add(res Result) {
	t.Cases++
	if res.Permissive {
		t.Permissive++
	}
	if res.Excluded != "" {
		t.Excluded++
		return
	}
	t.Counted++
	if res.Agree {
		t.Agree++
	}
	if res.Agree && res.Finer {
		t.Finer++
	}
}

// Key names a group of results.
type Key struct {
	Provenance Provenance
	Source     string
	Category   string // empty in the groups by provenance and source
}

// ByProvenance tallies the results of each provenance, all sources together: the numbers the
// acceptance holds to 98% (ADR-021 §6).
func (r *Report) ByProvenance() map[Provenance]Tally {
	out := map[Provenance]Tally{}
	for _, res := range r.Results {
		t := out[res.Case.Provenance]
		t.add(res)
		out[res.Case.Provenance] = t
	}
	return out
}

// By tallies the results by provenance and source, and with categories by provenance, source
// and category.
func (r *Report) By(categories bool) map[Key]Tally {
	out := map[Key]Tally{}
	for _, res := range r.Results {
		k := Key{Provenance: res.Case.Provenance, Source: res.Case.Source}
		if categories {
			k.Category = res.Case.Category
		}
		t := out[k]
		t.add(res)
		out[k] = t
	}
	return out
}

func sortedKeys(m map[Key]Tally) []Key {
	keys := make([]Key, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Provenance != b.Provenance {
			return a.Provenance.Rank() > b.Provenance.Rank()
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Category < b.Category
	})
	return keys
}

func row(cells ...string) string { return "| " + strings.Join(cells, " | ") + " |\n" }

func (t Tally) cells() []string {
	ratio := "—"
	if t.Counted > 0 {
		ratio = fmt.Sprintf("%.1f%%", t.Ratio())
	}
	return []string{fmt.Sprint(t.Cases), fmt.Sprint(t.Excluded), fmt.Sprint(t.Counted), fmt.Sprint(t.Agree),
		ratio, fmt.Sprint(t.Finer), fmt.Sprint(t.Permissive)}
}

var header = []string{"cases", "excluded", "counted", "agree", "ratio", "finer", "permissive"}

// Table returns the agreement by provenance and source, then by provenance alone, as Markdown.
func (r *Report) Table() string {
	var b strings.Builder
	b.WriteString(row(append([]string{"provenance", "source"}, header...)...))
	b.WriteString(row(strings.Split(strings.Repeat("---,", len(header)+2), ",")[:len(header)+2]...))
	by := r.By(false)
	for _, k := range sortedKeys(by) {
		b.WriteString(row(append([]string{string(k.Provenance), k.Source}, by[k].cells()...)...))
	}
	prov := r.ByProvenance()
	for _, p := range []Provenance{Deployed, TestAPI, Emulator} {
		if t, ok := prov[p]; ok {
			b.WriteString(row(append([]string{"**" + string(p) + "**", "**all**"}, t.cells()...)...))
		}
	}
	return b.String()
}

// CategoryTable returns the agreement by category, for the groups in which something counted
// disagrees, or for all when all is true.
func (r *Report) CategoryTable(all bool) string {
	var b strings.Builder
	b.WriteString(row(append([]string{"provenance", "source", "category"}, header...)...))
	b.WriteString(row(strings.Split(strings.Repeat("---,", len(header)+3), ",")[:len(header)+3]...))
	by := r.By(true)
	for _, k := range sortedKeys(by) {
		t := by[k]
		if !all && t.Agree == t.Counted && t.Permissive == 0 {
			continue
		}
		b.WriteString(row(append([]string{string(k.Provenance), k.Source, k.Category}, t.cells()...)...))
	}
	return b.String()
}

// Disagreements returns the counted results that disagree, permissive ones first, at most n
// (all when n <= 0).
func (r *Report) Disagreements(n int) []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Excluded == "" && !res.Agree || res.Permissive {
			out = append(out, res)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Permissive && !out[j].Permissive })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// ExcludedList returns the excluded cases grouped by reason, as Markdown: each reason with its
// count and case IDs (at most ten each).
func (r *Report) ExcludedList() string {
	byWhy := map[string][]string{}
	for _, res := range r.Results {
		if res.Excluded != "" {
			k := fmt.Sprintf("%s: %s", res.Case.Provenance, res.Excluded)
			byWhy[k] = append(byWhy[k], res.Case.ID)
		}
	}
	reasons := make([]string, 0, len(byWhy))
	for k := range byWhy {
		reasons = append(reasons, k)
	}
	sort.Strings(reasons)
	var b strings.Builder
	for _, k := range reasons {
		ids := byWhy[k]
		shown := ids
		if len(shown) > 10 {
			shown = shown[:10]
		}
		fmt.Fprintf(&b, "- %s (%d): `%s`", k, len(ids), strings.Join(shown, "`, `"))
		if len(ids) > len(shown) {
			fmt.Fprintf(&b, " and %d more", len(ids)-len(shown))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Stub returns the stub's message when every result that reached Compile met one of the
// contract's stubs, and "" otherwise: an engine that works on some cases is not the stub.
func (r *Report) Stub() string {
	msg := ""
	for _, res := range r.Results {
		if !res.Got.Tried {
			continue
		}
		if !res.Stub() {
			return ""
		}
		msg = res.Got.Panic
	}
	return msg
}

// Line formats one result for a list of disagreements.
func (res Result) Line() string {
	flag := ""
	if res.Permissive {
		flag = "PERMISSIVE "
	}
	return fmt.Sprintf("%s%s [%s, %s]: %s", flag, res.Case.ID, res.Case.Provenance, res.Case.File, res.Why)
}
