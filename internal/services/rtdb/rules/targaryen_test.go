package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// testdata/targaryen/fixtures.json is targaryen 3.1.0's test/spec/lib/parser/fixtures.json
// (github.com/goldibex/targaryen, commit e4151e7, ISC licence in the same folder): 186 rule
// expressions whose verdicts targaryen's authors recorded against Firebase. Each says whether
// the expression loads, fails when it runs, or what it evaluates to, for a user and some data.

type targaryenFixtures struct {
	Users map[string]map[string]any `json:"users"`
	Tests []struct {
		Rule          string            `json:"rule"`
		User          string            `json:"user"`
		IsValid       bool              `json:"isValid"`
		FailAtRuntime bool              `json:"failAtRuntime"`
		EvaluateTo    *bool             `json:"evaluateTo"`
		Wildchildren  map[string]string `json:"wildchildren"`
		Data          any               `json:"data"`
		Query         map[string]any    `json:"query"`
	} `json:"tests"`
}

// targaryenSkips are the fixtures Aruma answers differently on purpose.
var targaryenSkips = map[int]string{}

func TestTargaryenFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/targaryen/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx targaryenFixtures
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Tests) != 186 {
		t.Fatalf("%d fixtures, want 186", len(fx.Tests))
	}
	passed := 0
	for i, tc := range fx.Tests {
		t.Run(fmt.Sprintf("%03d", i), func(t *testing.T) {
			if why, ok := targaryenSkips[i]; ok {
				t.Skip(why)
			}
			auth := Unauthenticated()
			if u := fx.Users[tc.User]; u != nil {
				if auth, err = Variable(u); err != nil {
					t.Fatal(err)
				}
			}
			q := PlainREST // targaryen's default query is ordered by key
			if tc.Query != nil {
				q = queryFromMap(tc.Query)
			}
			got, loadErr, runErr := evalExpr(tc.Rule, kindRead, tc.Wildchildren, auth, tc.Data, q)
			switch {
			case !tc.IsValid:
				if loadErr == nil {
					t.Fatalf("%s: loads, want a load error", tc.Rule)
				}
			case loadErr != nil:
				t.Fatalf("%s: %v", tc.Rule, loadErr)
			case tc.FailAtRuntime:
				if runErr == nil {
					t.Fatalf("%s = %v, want a run-time failure", tc.Rule, got)
				}
			case runErr != nil:
				t.Fatalf("%s: %v", tc.Rule, runErr)
			case tc.EvaluateTo == nil || got != *tc.EvaluateTo:
				t.Fatalf("%s = %v, want %v", tc.Rule, got, *tc.EvaluateTo)
			}
			passed++
		})
	}
	t.Logf("targaryen fixtures: %d of %d pass, %d skipped", passed, len(fx.Tests), len(targaryenSkips))
}

func queryFromMap(m map[string]any) Query {
	var q Query
	for k, v := range m {
		switch k {
		case "orderByKey":
			q.OrderByKey = v == true
		case "orderByValue":
			q.OrderByValue = v == true
		case "orderByPriority":
			q.OrderByPriority = v == true
		case "orderByChild":
			q.OrderByChild, _ = v.(string)
		case "startAt":
			q.StartAt = v
		case "endAt":
			q.EndAt = v
		case "equalTo":
			q.EqualTo = v
		case "limitToFirst":
			q.LimitToFirst = int(v.(float64))
		case "limitToLast":
			q.LimitToLast = int(v.(float64))
		}
	}
	if !q.OrderByValue && !q.OrderByPriority && q.OrderByChild == "" {
		q.OrderByKey = true
	}
	return q
}

// evalExpr loads one expression as a rule at the root, with the wildcard variables bound, and
// evaluates it over data. It returns the value, a load error or a run-time failure.
func evalExpr(src string, kind ruleKind, vars map[string]string, auth Auth, data any, q Query) (any, error, error) {
	e, perr := parseExpr(src)
	if perr == nil {
		names := make([]string, 0, len(vars))
		for k := range vars {
			names = append(names, k)
		}
		perr = check(e, kind, names)
	}
	if perr != nil {
		return nil, fmt.Errorf("%d: %s", perr.pos, perr.msg), nil
	}
	src2, err := NewMemorySource(data)
	if err != nil {
		return nil, nil, err
	}
	w := &world{ctx: context.Background(), src: src2, cache: map[string]Node{}}
	ev := newEvaluator(w, Env{Auth: auth}, nil)
	ev.query = &q
	for k, v := range vars {
		ev.vars[k] = v
	}
	v, err := ev.eval(e)
	if err != nil {
		return nil, nil, err
	}
	return v, nil, nil
}
