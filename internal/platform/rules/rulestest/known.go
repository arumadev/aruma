package rulestest

import (
	"strings"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// Exclusion returns why a case does not count towards the agreement, or "" when it counts. The
// excluded cases are still run and listed: they are what the oracle cannot say about the
// engine, not what the engine may get wrong. Each reason is one of the classes of
// docs/compat/rules-v0.3.md: the case is not a decision, its source did not run it, it is a Test
// API artefact (parity-exceptions.md), or it is an emulator bug.
//
// The rules are about the case's data, so a new case of the same kind is excluded without a new
// entry; the IDs below are the cases no rule describes.
func Exclusion(c *Case) string {
	switch {
	case c.Expected.Outcome == Fact:
		return "not a decision: a value observed on deployed rules"
	case c.Skip != "":
		return "not run on the emulator: " + firstLine(c.Skip)
	case c.Ruleset == "":
		return "no ruleset: " + c.NoRuleset
	case c.Request == nil:
		return "no request: " + c.NoRequest
	case c.Expected.Outcome == Error && c.Expected.ErrorKind == "request" && !c.Expected.SourceTooLarge():
		return "the Test API refused the request itself, which is not a rules verdict: " + firstLine(c.Expected.Message)
	case c.Request.Method == "write" || c.Request.Method == "read":
		// The abstract verbs: allow read and allow write name them, but a real request is get,
		// list, create, update or delete. read was added after the one held-out run, where
		// aruma/methods/method_read_verb met it (docs/compat/rules-v0.3.md).
		return "Test API artefact: it takes the method " + c.Request.Method + ", which no real request has"
	}
	if c.Provenance == TestAPI {
		if strings.Contains(c.Expected.Message, "Function not found error: Name: [debug]") {
			return "Test API artefact: debug() is missing there; deployed rules return its argument, as the emulator does (production/deployed-firestore.json)"
		}
		if contradictoryMocks(c) {
			return "Test API artefact: get() and exists() of one document are mocked apart and disagree; a real document exists or not"
		}
		if c.Expected.Allows() && distinctLookups(c) > lookupLimit(c.Service) {
			return "Test API artefact: it applies no lookup limit to mocked documents"
		}
	}
	if c.Provenance == Emulator && strings.Contains(c.Expected.Message, "Unsupported operation error. Received: string(path).") {
		// Found after the one held-out run (aruma/storage-paths/v2_rest_string there).
		return "emulator artefact: the Storage rules runtime has no string() of a path; production gives the path's text (aruma/s1e-paths/rest_string)"
	}
	if why, ok := known[c.Provenance][c.ID]; ok {
		return why
	}
	return ""
}

// known are the excluded cases no rule of Exclusion describes, by provenance.
var known = map[Provenance]map[string]string{
	TestAPI: {
		"aruma/storage/fs_get_id":   "Test API artefact: a mocked get() result has no id; production names the document",
		"aruma/storage/fs_get_name": "Test API artefact: a mocked get() result has no __name__; production names the document",
	},
	Emulator: {
		"aruma/limits/exists_get_same_6":     "emulator bug: the Firestore emulator counts the lookups of both evaluations of a condition (firebase-tools #6252)",
		"aruma/storage/read_list":            "emulator binding: the Storage emulator matches a list against the children of the path",
		"aruma/storage/method_list":          "emulator binding: the Storage emulator matches a list against the children of the path",
		"aruma/storage-paths/list_on_folder": "emulator binding: the Storage emulator matches a list against the children of the path",
		"aruma/storage/fs_lookups_3":         "emulator artefact: the Storage emulator applies no lookup limit; deployed Storage denies the third document",
		"aruma/storage/fs_lookups_11":        "emulator artefact: the Storage emulator applies no lookup limit; deployed Storage denies the third document",
	},
}

func lookupLimit(svc rules.Service) int {
	lim := rules.DefaultLimits()
	if svc == rules.Storage {
		return lim.StorageLookups
	}
	return lim.LookupsPerOperation
}

// distinctLookups counts the distinct documents a case mocks.
func distinctLookups(c *Case) int {
	seen := map[string]bool{}
	for _, m := range c.Mocks {
		_, after, _ := mockTarget(m.Function)
		k := m.Path
		if after {
			k = "after:" + k
		}
		seen[k] = true
	}
	return len(seen)
}

// contradictoryMocks reports whether a case mocks get() of a document with a document and
// exists() of it as false, or get() as null and exists() as true. The Test API answers each
// function from its own mock (Pyric simcase-mock-and-path-fidelity, "exists mocked false with a
// get mock"); the engine reads a document once for both, as a service does.
func contradictoryMocks(c *Case) bool {
	type doc struct {
		path  string
		after bool
	}
	got, exists := map[doc]bool{}, map[doc]bool{}
	for _, m := range c.Mocks {
		get, after, err := mockTarget(m.Function)
		if err != nil || !m.Result.Present() {
			continue
		}
		d := doc{strings.TrimPrefix(m.Path, "/"), after}
		if get {
			got[d] = !m.Result.IsNull()
			continue
		}
		exists[d] = strings.TrimSpace(string(m.Result.Raw())) == "true"
	}
	for d, found := range got {
		if e, ok := exists[d]; ok && e != found {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
