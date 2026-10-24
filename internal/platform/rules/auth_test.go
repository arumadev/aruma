package rules

import (
	"encoding/json"
	"testing"
	"time"
)

// request.auth: uid and token; a whole number that fits an int64 is an int, any other number a
// float, as the Rules Test API reads a token (the review's probes, 2026-10-24; provisional until
// deployed rules are measured).
func TestAuth(t *testing.T) {
	var claims map[string]any
	if err := json.Unmarshal([]byte(`{
		"exp": 1761300000, "iat": 1761296400, "auth_time": 1761296000,
		"email": "a@example.com", "email_verified": true, "level": 2, "level2": 2.0, "ratio": 0.5,
		"roles": ["admin", 1], "nothing": null,
		"firebase": {"sign_in_provider": "password", "identities": {"email": ["a@example.com"]}, "n": 3}
	}`), &claims); err != nil {
		t.Fatal(err)
	}
	claims["when"] = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	claims["typed"] = int64(7)
	claims["skipped"] = struct{}{}
	auth := Auth("alice", claims)
	token := func(path ...string) Value {
		v, err := callMethod(newTestMeter(), auth, "get", []Value{List(append([]Value{String("token")}, strs(path)...)), Null()})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		path []string
		want Value
	}{
		{[]string{"exp"}, vi(1761300000)},
		{[]string{"iat"}, vi(1761296400)},
		{[]string{"auth_time"}, vi(1761296000)},
		{[]string{"email"}, vs("a@example.com")},
		{[]string{"email_verified"}, vb(true)},
		{[]string{"level"}, vi(2)},
		{[]string{"level2"}, vi(2)},
		{[]string{"ratio"}, vf(0.5)},
		{[]string{"roles"}, vl(vs("admin"), vi(1))},
		{[]string{"nothing"}, vn()},
		{[]string{"firebase", "sign_in_provider"}, vs("password")},
		{[]string{"firebase", "identities", "email"}, vl(vs("a@example.com"))},
		{[]string{"firebase", "n"}, vi(3)},
		{[]string{"when"}, vts("2025-01-02T03:04:05Z")},
		{[]string{"typed"}, vi(7)},
	} {
		if got := token(c.path...); !sameValue(got, c.want) {
			t.Errorf("token.%v = %s, want %s", c.path, showValue(got), showValue(c.want))
		}
	}
	if uid, _ := selectField(newTestMeter(), auth, "uid"); !sameValue(uid, vs("alice")) {
		t.Errorf("uid = %s", showValue(uid))
	}
	if _, err := selectField(newTestMeter(), token(), "skipped"); err == nil {
		t.Error("a claim of an unknown type is kept")
	}
	// A number that is not whole, or past int64, is a float; json.Number reads as its value.
	other := Auth("bob", map[string]any{"exp": 1.5, "iat": json.Number("12"), "n": json.Number("1.25"), "u": uint64(1 << 63), "w": json.Number("2.0"), "big": 1e19})
	tok, _ := selectField(newTestMeter(), other, "token")
	for k, want := range map[string]Value{"exp": vf(1.5), "iat": vi(12), "n": vf(1.25), "u": vf(1 << 63), "w": vi(2), "big": vf(1e19)} {
		if got, _ := selectField(newTestMeter(), tok, k); !sameValue(got, want) {
			t.Errorf("%s = %s, want %s", k, showValue(got), showValue(want))
		}
	}
	// Deployed rules, a real ID token (S7-R, production/deployed-firestore.json): a whole claim
	// is an int even beyond 2^53, exactly, when the verifier keeps its text (json.Number).
	deployed := Auth("u", map[string]any{"big": json.Number("9007199254740993"), "huge": json.Number("1e20"), "neg": json.Number("-7"),
		"m": map[string]any{"n": json.Number("3"), "f": json.Number("3.5")}, "l": []any{json.Number("3"), json.Number("3.5")}})
	tok, _ = selectField(newTestMeter(), deployed, "token")
	for k, want := range map[string]Value{"big": vi(9007199254740993), "huge": vf(1e20), "neg": vi(-7),
		"m": vm("f", vf(3.5), "n", vi(3)), "l": vl(vi(3), vf(3.5))} {
		if got, _ := selectField(newTestMeter(), tok, k); !sameValue(got, want) {
			t.Errorf("%s = %s, want %s", k, showValue(got), showValue(want))
		}
	}
	if empty := Auth("", nil); !sameValue(empty, vm("token", vm(), "uid", vs(""))) {
		t.Errorf("Auth with no claims = %s", showValue(empty))
	}
}

func strs(ss []string) []Value {
	out := make([]Value, len(ss))
	for i, s := range ss {
		out[i] = String(s)
	}
	return out
}
