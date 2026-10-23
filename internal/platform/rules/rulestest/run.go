package rulestest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// Got is what the engine made of a case.
type Got struct {
	Compiled bool
	Issues   rules.Issues
	Ran      bool // Evaluate was called and returned
	Decision rules.Decision
	Err      error  // Evaluate's error: an outage, never a verdict
	Panic    string // a panic in Compile or Evaluate, such as the contract's stubs
	Outcome  Outcome
}

// FirstError returns the first error issue of a failed compile.
func (g Got) FirstError() (rules.Issue, bool) {
	for _, i := range g.Issues {
		if i.Severity == rules.SeverityError {
			return i, true
		}
	}
	return rules.Issue{}, false
}

// Result is one case run through the engine and compared with its expected result.
type Result struct {
	Case     *Case
	Excluded string // why the case does not count; "" when it counts
	Got      Got
	// Agree is the verdict the acceptance counts (ADR-021 §6): for an evaluation, allowed or
	// not; for a compile, compiled or not and, when the source gives one, the first error's
	// line and column.
	Agree bool
	// Finer is whether the finer result agrees too: allow, deny or error for an evaluation, and
	// the first error's message for a compile. It is reported, not counted.
	Finer bool
	// Permissive is an allow where the case's source, and every source above it, denied. It
	// fails the acceptance whatever the ratio.
	Permissive bool
	Why        string // what differs, when Agree or Finer is false
}

// Stub reports whether the result met one of the contract's stubs (a panic "S1-…: …").
func (r Result) Stub() bool { return strings.HasPrefix(r.Got.Panic, "S1-") }

// Runner runs cases through the engine, compiling each distinct rules file once.
type Runner struct {
	Limits   rules.Limits
	Inflight *rules.Inflight

	mu    sync.Mutex
	cache map[compileKey]compiled
}

type compileKey struct {
	svc  rules.Service
	name string
	src  string
}

type compiled struct {
	rs     *rules.Ruleset
	issues rules.Issues
	panic  string
}

// NewRunner returns a Runner with the default limits.
func NewRunner() *Runner {
	lim := rules.DefaultLimits()
	return &Runner{Limits: lim, Inflight: rules.NewInflight(lim.InflightBytes), cache: map[compileKey]compiled{}}
}

// Compile compiles a file for a service, once per distinct file. A panic is returned, not
// raised, so that a stub reports instead of stopping the run.
func (r *Runner) Compile(svc rules.Service, f rules.File) (*rules.Ruleset, rules.Issues, string) {
	k := compileKey{svc, f.Name, f.Content}
	r.mu.Lock()
	c, ok := r.cache[k]
	r.mu.Unlock()
	if ok {
		return c.rs, c.issues, c.panic
	}
	func() {
		defer func() {
			if p := recover(); p != nil {
				c = compiled{panic: fmt.Sprint(p)}
			}
		}()
		c.rs, c.issues = rules.Compile(svc, f, r.Limits)
	}()
	r.mu.Lock()
	r.cache[k] = c
	r.mu.Unlock()
	return c.rs, c.issues, c.panic
}

// Evaluate evaluates a request, returning a panic instead of raising it.
func Evaluate(ctx context.Context, rs *rules.Ruleset, req *rules.Request) (d rules.Decision, panicked string, err error) {
	defer func() {
		if p := recover(); p != nil {
			panicked = fmt.Sprint(p)
		}
	}()
	d, err = rs.Evaluate(ctx, req)
	return d, "", err
}

// OutcomeOf classifies a decision as the oracle does: allow; error when a statement evaluated to
// an error; deny otherwise (a false condition, or no statement that matches).
func OutcomeOf(d rules.Decision) Outcome {
	if d.Allow {
		return Allow
	}
	for _, s := range d.Statements {
		if s.Outcome == rules.OutcomeError {
			return Error
		}
	}
	return Deny
}

// Run runs one case and compares it with its expected result.
func (r *Runner) Run(ctx context.Context, c *Case) Result {
	res := Result{Case: c, Excluded: Exclusion(c)}
	if c.Ruleset == "" {
		res.Why = "nothing to run"
		return res
	}
	file, req, berr := c.Build(r.Inflight)
	var ne *NotExpressibleError
	if berr != nil && !errors.As(berr, &ne) {
		res.Why = "binding: " + berr.Error()
		return res
	}
	if ne != nil && res.Excluded == "" {
		res.Excluded = "not expressible: " + ne.Reason
	}

	g := &res.Got
	var rs *rules.Ruleset
	rs, g.Issues, g.Panic = r.Compile(c.Service, file)
	g.Compiled = rs != nil
	if g.Panic == "" && rs != nil && req != nil {
		g.Decision, g.Panic, g.Err = Evaluate(ctx, rs, req)
		g.Ran = g.Panic == ""
		if g.Ran && g.Err == nil {
			g.Outcome = OutcomeOf(g.Decision)
		}
	}
	res.compare()
	return res
}

func (res *Result) compare() {
	c, g, exp := res.Case, &res.Got, res.Case.Expected
	switch {
	case g.Panic != "":
		res.Why = "panic: " + g.Panic
		return
	case g.Err != nil:
		res.Why = "Evaluate failed: " + g.Err.Error()
		return
	}
	allowed := g.Ran && g.Decision.Allow
	// An excluded case's verdict is not production's (debug() missing, a mock without id), so
	// allowing what it denied is not more permissive than production.
	res.Permissive = res.Excluded == "" && allowed && !exp.Allows() && (c.Higher == nil || !c.Higher.Allows())

	if exp.Compile() || exp.SourceTooLarge() {
		if g.Compiled {
			res.Why = "compiles; the source rejected it: " + firstLine(exp.Message)
			return
		}
		res.Agree, res.Finer = true, true
		first, _ := g.FirstError()
		if line, col, desc, ok := exp.FirstError(); ok {
			if first.Position == nil || first.Position.Line != line || first.Position.Column != col {
				res.Agree, res.Finer = false, false
				res.Why = fmt.Sprintf("first error at %s, the source's at L%d:%d %s", positionOf(first), line, col, desc)
				return
			}
			if first.Description != desc {
				res.Finer = false
				res.Why = fmt.Sprintf("message %q, the source's %q", first.Description, desc)
			}
		}
		return
	}
	if !g.Compiled {
		first, _ := g.FirstError()
		res.Why = "does not compile: " + first.String()
		return
	}
	if !g.Ran {
		res.Why = "not evaluated"
		return
	}
	res.Agree = allowed == exp.Allows()
	res.Finer = res.Agree && (g.Outcome == exp.Outcome || exp.Outcome == Error && exp.ErrorKind != "evaluation")
	if !res.Agree || !res.Finer {
		res.Why = fmt.Sprintf("%s, the source's %s", describe(g), describeExpected(exp))
	}
}

func positionOf(i rules.Issue) string {
	if i.Position == nil {
		return "no position"
	}
	return fmt.Sprintf("L%d:%d", i.Position.Line, i.Position.Column)
}

func describe(g *Got) string {
	s := string(g.Outcome)
	for _, st := range g.Decision.Statements {
		if st.Outcome == rules.OutcomeError && st.Message != "" {
			return s + " (" + st.Message + ")"
		}
	}
	return s
}

func describeExpected(e Expected) string {
	s := string(e.Outcome)
	if e.Message != "" {
		s += " (" + firstLine(e.Message) + ")"
	}
	return s
}
