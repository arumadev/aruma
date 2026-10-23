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
		return "not a decision: a value observed on deployed Storage"
	case c.Skip != "":
		return "not run on the emulator: " + firstLine(c.Skip)
	case c.Ruleset == "":
		return "no ruleset: " + c.NoRuleset
	case c.Request == nil:
		return "no request: " + c.NoRequest
	case c.Expected.Outcome == Error && c.Expected.ErrorKind == "request" && !c.Expected.SourceTooLarge():
		return "the Test API refused the request itself, which is not a rules verdict: " + firstLine(c.Expected.Message)
	case c.Request.Method == "write":
		return "Test API artefact: it takes the method write, which no real request has"
	}
	if c.Provenance == TestAPI {
		if strings.Contains(c.Expected.Message, "Function not found error: Name: [debug]") {
			return "Test API artefact: debug() is missing there; the emulator and production have it"
		}
		if c.Expected.Allows() && distinctLookups(c) > lookupLimit(c.Service) {
			return "Test API artefact: it applies no lookup limit to mocked documents"
		}
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
