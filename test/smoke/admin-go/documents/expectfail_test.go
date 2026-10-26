package documents

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// listFile is the expected-failure list for this suite (ADR-028 §7.3): a test named in it must
// fail, with the listed reason in its failure when one is given, or this suite fails. A listed
// test that passes, or a listed name that no test carries, fails the run too, so no entry outlives
// the gap it records.
//
// One entry per line: the full test name (as t.Name() prints it), then optionally " :: " and a
// text the failure must contain. # starts a comment.
const listFile = "../../../compat/documents/expected-failures/admin-go.txt"

// T is what a test body uses of *testing.T. A listed test runs against a recorder instead, which
// collects the failure rather than reporting it.
type T interface {
	Helper()
	Name() string
	Context() context.Context
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Fatal(args ...any)
}

var (
	expected map[string]string // test name -> reason ("" for any failure)
	seenMu   sync.Mutex
	seen     = map[string]bool{}
)

func loadExpected() error {
	f, err := os.Open(listFile)
	if err != nil {
		return err
	}
	defer f.Close()
	expected = map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, reason, _ := strings.Cut(line, " :: ")
		name, reason = strings.TrimSpace(name), strings.TrimSpace(reason)
		if _, dup := expected[name]; dup {
			return fmt.Errorf("%s:%d: %s is listed twice", listFile, n, name)
		}
		expected[name] = reason
	}
	return sc.Err()
}

// checkStale reports listed names no test ran under, unless -run or -skip narrowed the run.
func checkStale() []string {
	for _, name := range []string{"test.run", "test.skip"} {
		if f := flag.Lookup(name); f != nil && f.Value.String() != "" {
			return nil
		}
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	var stale []string
	for name := range expected {
		if !seen[name] {
			stale = append(stale, name)
		}
	}
	slices.Sort(stale)
	return stale
}

// check runs body as the subtest name of t, through the expected-failure list.
func check(t *testing.T, name string, body func(t T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		full := t.Name()
		seenMu.Lock()
		seen[full] = true
		seenMu.Unlock()
		reason, listed := expected[full]
		if !listed {
			body(t)
			return
		}
		r := &recorder{name: full, ctx: t.Context()}
		done := make(chan struct{})
		go func() {
			defer close(done)
			body(r)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Minute):
			t.Fatalf("listed in %s, and still running after two minutes", listFile)
		}
		switch {
		case !r.failed:
			t.Errorf("%s is listed in %s but passed: remove it from the list", full, listFile)
		case reason != "" && !strings.Contains(r.log.String(), reason):
			t.Errorf("listed in %s to fail with %q, but failed otherwise:\n%s", listFile, reason, r.log.String())
		default:
			t.Logf("expected failure (%s):\n%s", listFile, r.log.String())
		}
	})
}

// recorder is a T that keeps failures to itself. Fatal ends the body's goroutine, as on a
// *testing.T.
type recorder struct {
	name   string
	ctx    context.Context
	mu     sync.Mutex
	failed bool
	log    strings.Builder
}

func (r *recorder) Helper()                  {}
func (r *recorder) Name() string             { return r.name }
func (r *recorder) Context() context.Context { return r.ctx }

func (r *recorder) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(&r.log, "    "+format+"\n", args...)
}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	r.failed = true
	r.mu.Unlock()
	r.Logf(format, args...)
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}

func (r *recorder) Fatal(args ...any) {
	r.Errorf("%s", fmt.Sprint(args...))
	runtime.Goexit()
}
