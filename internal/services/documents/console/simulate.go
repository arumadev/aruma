package console

import (
	"cmp"
	"encoding/json"
	"net/http"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/documents/api"
)

// simulateBody is a Rules Playground request (f7-production-console.md §2).
type simulateBody struct {
	// Source is the editor's text, published or not; absent, the published rules.
	Source *string `json:"source"`
	// Method is get, create, update or delete: the Playground has no list.
	Method string `json:"method"`
	// Path is the document's, from the database's documents root (users/u1).
	Path string `json:"path"`
	// Auth, when present, is the signed-in user the Playground's "Authenticated" switch makes;
	// every field is optional.
	Auth *simulateAuth `json:"auth"`
	// Data is request.resource's data of a create or an update, in REST form.
	Data json.RawMessage `json:"data"`
}

type simulateAuth struct {
	UID           string `json:"uid"`
	Provider      string `json:"provider"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	Name          string `json:"name"`
	PhoneNumber   string `json:"phoneNumber"`
}

// claims are the token's claims the Playground builds, as its "Authentication payload" shows
// them: sub the uid, aud the project, the profile's fields even empty, and the provider
// (google.com unless given) as firebase.sign_in_provider.
func (a simulateAuth) claims(project string) map[string]any {
	return map[string]any{
		"sub":            a.UID,
		"aud":            project,
		"email":          a.Email,
		"email_verified": a.EmailVerified,
		"phone_number":   a.PhoneNumber,
		"name":           a.Name,
		"firebase":       map[string]any{"sign_in_provider": cmp.Or(a.Provider, "google.com")},
	}
}

var simulateMethods = map[string]rules.Method{
	"get": rules.MethodGet, "create": rules.MethodCreate, "update": rules.MethodUpdate, "delete": rules.MethodDelete,
}

// statementJSON is an allow statement the rules evaluated: where it is, the methods it names,
// and what its condition came to (true, false or error, with the error's message).
type statementJSON struct {
	Line    int      `json:"line"`
	Column  int      `json:"column"`
	Methods []string `json:"methods"`
	Outcome string   `json:"outcome"`
	Message string   `json:"message,omitempty"`
}

// simulateRules is the Rules Playground: it judges a get, create, update or delete of a
// document by the source given (or the published rules) as a real request of the user
// described would be judged, the document as stored read as resource and get() and exists()
// reading the project's documents, and writes nothing (api.Service.SimulateRules). It answers
// allowed or denied, with each allow statement evaluated, its line and column and outcome:
// which granted the request, or why none did. A source that does not compile is refused as
// putRules refuses it, with its issues' lines and columns.
func (h *handlers) simulateRules(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in simulateBody
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	method, ok := simulateMethods[in.Method]
	if !ok {
		h.m.Fail(w, r, apierr.Invalid("method", "method must be get, create, update or delete."))
		return
	}
	rel, err := relPath(in.Path, documentPath)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	sim := api.Simulation{Document: t.name(rel), Method: method}
	if method == rules.MethodCreate || method == rules.MethodUpdate {
		if sim.Fields, err = fieldsOf(in.Data); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	if in.Auth != nil {
		sim.Authenticated, sim.AuthUID, sim.AuthToken = true, in.Auth.UID, in.Auth.claims(t.project.ID)
	}
	var issues rules.Issues
	if in.Source != nil {
		if sim.Ruleset, issues, err = h.opts.Rules.CompileRules(r.Context(), rules.Firestore,
			rules.File{Name: rulesFile, Content: *in.Source}); err != nil {
			h.m.Fail(w, r, err)
			return
		}
		if sim.Ruleset == nil {
			h.m.Fail(w, r, rulesInvalid(issues))
			return
		}
	}
	res, err := h.svc.SimulateRules(r.Context(), t.call, sim)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	statements := make([]statementJSON, 0, len(res.Statements))
	for _, st := range res.Statements {
		statements = append(statements, statementJSON{Line: st.Position.Line, Column: st.Position.Column,
			Methods: st.Methods, Outcome: st.Outcome.String(), Message: st.Message})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"allowed": res.Allow, "statements": statements, "issues": issuesJSON(issues),
	})
}
