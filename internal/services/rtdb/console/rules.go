package console

import (
	"errors"
	"net/http"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/rtdb/core"
	"github.com/arumadev/aruma/internal/services/rtdb/rules"
)

// rulesRoutes registers the rules editor's routes.
func (h *handlers) rulesRoutes() {
	const path = "/console/v1/projects/{project}/database/rules"
	h.m.Handle(anyCred, "GET", path, h.getRules)
	h.m.Handle(anyCred, "PUT", path, h.putRules)
}

// rulesErrorType is the @type of the detail that lists a rules document's errors.
const rulesErrorType = "type.aruma.dev/rtdb.RulesErrors"

// ruleError is one problem in a rules document, where Firebase would report it: 1-based line
// and column in characters, and the rule it is in ("rules/users/$uid/.write"), if any.
type ruleError struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// rulesInvalid is the error for a document that does not load: INVALID_ARGUMENT, a BadRequest
// violation on "rules" with the first problem, and every problem with its position.
func rulesInvalid(err error) *apierr.Error {
	var list []ruleError
	var one *rules.Error
	var many rules.Errors
	switch {
	case errors.As(err, &many):
		for _, e := range many {
			list = append(list, ruleError{e.Line, e.Column, e.Path, e.Msg})
		}
	case errors.As(err, &one):
		list = append(list, ruleError{one.Line, one.Column, one.Path, one.Msg})
	default:
		list = append(list, ruleError{1, 1, "", err.Error()})
	}
	first := list[0]
	return apierr.New(apierr.InvalidArgument, "The rules are not valid: %d:%d: %s", first.Line, first.Column, first.Message).
		WithDetails(apierr.BadRequest(apierr.FieldViolation{Field: "rules", Description: err.Error()}), struct {
			Type   string      `json:"@type"`
			Errors []ruleError `json:"errors"`
		}{rulesErrorType, list})
}

func (h *handlers) getRules(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	doc, err := h.svc.Rules(r.Context(), t)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"rules": doc})
}

// putRules checks a rules document and, once it loads, saves it as written (comments kept)
// the way REST's PUT /.settings/rules.json does: the database's writer takes the new rules in
// its order. A document that does not load is refused with every error and its position.
func (h *handlers) putRules(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in struct {
		Rules string `json:"rules"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if _, err := rules.Parse([]byte(in.Rules)); err != nil {
		h.m.Fail(w, r, rulesInvalid(err))
		return
	}
	if err := h.svc.SetRules(r.Context(), t, in.Rules); err != nil {
		if e := core.AsError(err); e.HTTP == http.StatusRequestEntityTooLarge {
			h.m.Fail(w, r, apierr.Invalid("rules", "The rules are larger than this server accepts."))
			return
		}
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"rules": in.Rules})
}
