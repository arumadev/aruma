package console

import (
	"net/http"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// rulesRoutes registers the rules editor's routes. They go through tenancy, like firebase deploy
// --only storage: a ruleset saved and the release firebase.storage/<bucket> pointed at it,
// both audited by tenancy (rules.ruleset.create, rules.release.publish) in their transactions.
func (h *handlers) rulesRoutes() {
	const path = "/console/v1/projects/{project}/storage/rules"
	h.handle("GET", path, h.getRules)
	h.handle("PUT", path, h.putRules)
	h.handle("POST", path+":validate", h.validateRules)
}

// rulesFile is the file name a dashboard's ruleset is saved under, firebase-tools' default.
const rulesFile = "storage.rules"

// rulesIssuesType is the @type of the detail that lists a source's issues.
const rulesIssuesType = "type.aruma.dev/storage.RulesIssues"

// issueJSON is one issue as the firebaserules API writes it (issues[]), so the editor marks
// its lines as it would for firebase deploy.
type issueJSON struct {
	SourcePosition *positionJSON `json:"sourcePosition,omitempty"`
	Description    string        `json:"description"`
	Severity       string        `json:"severity"`
}

type positionJSON struct {
	FileName      string `json:"fileName"`
	Line          int    `json:"line"`
	Column        int    `json:"column"`
	CurrentOffset int    `json:"currentOffset"`
	EndOffset     int    `json:"endOffset"`
}

func issuesJSON(is rules.Issues) []issueJSON {
	out := make([]issueJSON, 0, len(is))
	for _, i := range is {
		j := issueJSON{Description: i.Description, Severity: i.Severity.String()}
		if p := i.Position; p != nil {
			j.SourcePosition = &positionJSON{FileName: i.File, Line: p.Line, Column: p.Column,
				CurrentOffset: p.CurrentOffset, EndOffset: p.EndOffset}
		}
		out = append(out, j)
	}
	return out
}

// rulesInvalid is the error for a source that does not compile: INVALID_ARGUMENT with the
// first error, and every issue with its position in a detail.
func rulesInvalid(is rules.Issues) *apierr.Error {
	msg := "The rules do not compile."
	for _, i := range is {
		if i.Severity == rules.SeverityError {
			msg = "The rules do not compile: " + i.String()
			break
		}
	}
	return apierr.New(apierr.InvalidArgument, "%s", msg).WithDetails(struct {
		Type   string      `json:"@type"`
		Issues []issueJSON `json:"issues"`
	}{rulesIssuesType, issuesJSON(is)})
}

// release is the name the Storage release of the project's bucket goes by in the API.
func release(bucket string) string { return "firebase.storage/" + bucket }

type sourceBody struct {
	Source string `json:"source"`
}

// getRules returns the published Storage rules: the release's ruleset and its source. A project
// that never published any runs the locked default (ADR-021 §7): published is false.
func (h *handlers) getRules(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rel, err := h.opts.Rules.GetRelease(r.Context(), t.caller, t.project.ID, release(t.bucket.Name))
	switch {
	case apierr.CodeOf(err) == apierr.NotFound:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"published": false, "source": ""})
		return
	case err != nil:
		h.m.Fail(w, r, err)
		return
	}
	rs, err := h.opts.Rules.GetRuleset(r.Context(), t.caller, t.project.ID, rel.RulesetName)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	source := ""
	if len(rs.Files) > 0 {
		source = rs.Files[0].Content
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"published": true, "source": source, "rulesetName": rel.RulesetName, "updateTime": wire.Time(rel.UpdateTime),
	})
}

// putRules saves a source as a ruleset and publishes it as the bucket's release, so /v0 applies
// it on the next request. A source that does not compile is refused with its issues, and
// nothing is saved; the warnings of one that compiles come back with it.
func (h *handlers) putRules(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in sourceBody
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rs, issues, err := h.opts.Rules.CreateRuleset(r.Context(), t.caller, t.project.ID, rules.Storage,
		[]rules.File{{Name: rulesFile, Content: in.Source}})
	if err != nil {
		if issues.Err() != nil {
			err = rulesInvalid(issues)
		}
		h.m.Fail(w, r, err)
		return
	}
	rel, err := h.opts.Rules.PublishRelease(r.Context(), t.caller, t.project.ID, release(t.bucket.Name), rs.Name)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"published": true, "source": in.Source, "rulesetName": rel.RulesetName, "updateTime": wire.Time(rel.UpdateTime),
		"issues": issuesJSON(issues),
	})
}

// validateRules compiles a source without saving it, and returns its issues: the editor's
// check before it publishes.
//
// It compiles under the default limits: the routes are not given the configured ones
// (console.Options), so a server whose rules limits are tighter than the defaults may refuse at
// publish a source that passed here; publishing compiles it again under the configured limits.
func (h *handlers) validateRules(w http.ResponseWriter, r *http.Request) {
	if _, err := h.target(r); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in sourceBody
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rs, issues := rules.Compile(rules.Storage, rules.File{Name: rulesFile, Content: in.Source}, rules.DefaultLimits())
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"valid": rs != nil, "issues": issuesJSON(issues)})
}
