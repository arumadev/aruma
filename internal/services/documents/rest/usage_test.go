package rest_test

import (
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
)

// TestUsage: over REST, the documents written, deleted and read (a get, a missing get, a list
// page, a query with no result) are counted as Firestore bills them, with the rules' decisions.
func TestUsage(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	h.env.PublishFirestoreRules(t, `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /u/{d} { allow read, write: if request.auth != null; }
  }
}`)
	tok := apitest.UserToken(t, "alice", nil)
	for _, c := range []struct {
		method, path, body string
		token              string
		code               int
	}{
		{"PATCH", "/v1/" + root + "/u/a", `{"fields":{"n":{"integerValue":"1"}}}`, tok, 200},
		{"PATCH", "/v1/" + root + "/u/b", `{"fields":{"n":{"integerValue":"2"}}}`, tok, 200},
		{"DELETE", "/v1/" + root + "/u/b", "", tok, 200},
		{"GET", "/v1/" + root + "/u/a", "", tok, 200},
		{"GET", "/v1/" + root + "/u/missing", "", tok, 404},
		{"GET", "/v1/" + root + "/u", "", tok, 200},
		{"POST", "/v1/" + root + ":runQuery", `{"structuredQuery":{"from":[{"collectionId":"u"}],"where":{"fieldFilter":{"field":{"fieldPath":"n"},"op":"EQUAL","value":{"integerValue":"9"}}}}}`, tok, 200},
		{"GET", "/v1/" + root + "/u/a", "", "", 403},
	} {
		if code, _, body := h.do(t, c.method, c.path, c.body, c.token); code != c.code {
			t.Fatalf("%s %s: %d %s", c.method, c.path, code, body)
		}
	}
	hs, err := h.env.Usage.Hours(t.Context(), h.env.Number, time.Now())
	if err != nil || len(hs) != 1 {
		t.Fatalf("usage: %+v %v", hs, err)
	}
	// Reads: the get, the missing get, the page of one, the empty query's one.
	if u := hs[0]; u.Writes != 2 || u.Deletes != 1 || u.Reads != 4 || u.RulesAllows != 7 || u.RulesDenies != 1 {
		t.Errorf("usage: %+v", u)
	}
}
