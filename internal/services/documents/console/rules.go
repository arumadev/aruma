package console

import (
	"net/http"
	"strconv"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/documents/api"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// rulesRoutes registers the rules editor's routes. They go through tenancy, like firebase deploy
// --only firestore:rules: a ruleset saved and the release cloud.firestore pointed at it, both
// audited by tenancy (rules.ruleset.create, rules.release.publish) in their transactions. The
// service layer reads the release on the next request.
func (h *handlers) rulesRoutes() {
	const path = base + "/rules"
	h.handle("GET", path, h.getRules)
	h.handle("PUT", path, h.putRules)
	h.handle("POST", path+":validate", h.validateRules)
	h.handle("GET", base+"/rulesets", h.listRulesets)
	h.handle("GET", base+"/rulesets/{ruleset}", h.getRuleset)
}

// rulesFile is the file name a dashboard's ruleset is saved under, firebase-tools' default.
const rulesFile = "firestore.rules"

// rulesIssuesType is the @type of the detail that lists a source's issues.
const rulesIssuesType = "type.aruma.dev/documents.RulesIssues"

// issueJSON is one issue as the firebaserules API writes it (issues[]), so the editor marks its
// lines as it would for firebase deploy: line and column are 1-based.
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

// rulesInvalid is the error for a source that does not compile: INVALID_ARGUMENT with the first
// error, and every issue with its position in a detail.
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

type sourceBody struct {
	Source string `json:"source"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// getRules returns the published Firestore rules: the release's ruleset and its source. A
// project that never published any has the locked default, which denies every client: published
// is false.
func (h *handlers) getRules(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rel, err := h.opts.Rules.GetRelease(r.Context(), t.caller, t.project.ID, api.FirestoreRelease)
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"published": true, "source": source(rs), "rulesetName": rel.RulesetName, "updateTime": rfc3339(rel.UpdateTime),
	})
}

func source(rs tcore.Ruleset) string {
	if len(rs.Files) == 0 {
		return ""
	}
	return rs.Files[0].Content
}

// putRules saves a source as a ruleset and publishes it as cloud.firestore, so the next request
// is judged by it. A source that does not compile is refused with its issues, and nothing is
// saved; the warnings of one that compiles come back with it.
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
	rs, issues, err := h.opts.Rules.CreateRuleset(r.Context(), t.caller, t.project.ID, rules.Firestore,
		[]rules.File{{Name: rulesFile, Content: in.Source}})
	if err != nil {
		if issues.Err() != nil {
			err = rulesInvalid(issues)
		}
		h.m.Fail(w, r, err)
		return
	}
	rel, err := h.opts.Rules.PublishRelease(r.Context(), t.caller, t.project.ID, api.FirestoreRelease, rs.Name)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"published": true, "source": in.Source, "rulesetName": rel.RulesetName, "updateTime": rfc3339(rel.UpdateTime),
		"issues": issuesJSON(issues),
	})
}

// validateRules compiles a source without saving it, and returns its issues with their lines
// and columns: the editor's check before it publishes.
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
	rs, issues, err := h.opts.Rules.CompileRules(r.Context(), rules.Firestore, rules.File{Name: rulesFile, Content: in.Source})
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"valid": rs != nil, "issues": issuesJSON(issues)})
}

// maxRulesets bounds a page of the rules history.
const maxRulesets = 50

// listRulesets returns a page of the project's Firestore rulesets, newest first, the one
// cloud.firestore points to marked current: the history the Firebase console shows beside its
// editor. A page may hold fewer than pageSize, since Storage's rulesets are left out of it.
func (h *handlers) listRulesets(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	size := 20
	if v := r.URL.Query().Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			h.m.Fail(w, r, apierr.Invalid("pageSize", "pageSize must be a positive number."))
			return
		}
		size = min(n, maxRulesets)
	}
	page, err := h.opts.Rules.ListRulesets(r.Context(), t.caller, t.project.ID,
		tcore.ListOptions{PageSize: size, PageToken: r.URL.Query().Get("pageToken")})
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	current := ""
	rel, err := h.opts.Rules.GetRelease(r.Context(), t.caller, t.project.ID, api.FirestoreRelease)
	switch {
	case err == nil:
		current = rel.RulesetName
	case apierr.CodeOf(err) != apierr.NotFound:
		h.m.Fail(w, r, err)
		return
	}
	items := []map[string]any{}
	for _, rs := range page.Items {
		if rs.Service != rules.Firestore {
			continue
		}
		items = append(items, map[string]any{"name": rs.Name, "createTime": rfc3339(rs.CreateTime), "current": rs.Name == current})
	}
	out := map[string]any{"rulesets": items}
	if page.NextPageToken != "" {
		out["nextPageToken"] = page.NextPageToken
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getRuleset returns one of the project's Firestore rulesets with its source. {ruleset} is its
// id (the uuid of projects/{p}/rulesets/{uuid}).
func (h *handlers) getRuleset(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rs, err := h.opts.Rules.GetRuleset(r.Context(), t.caller, t.project.ID, r.PathValue("ruleset"))
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if rs.Service != rules.Firestore {
		h.m.Fail(w, r, apierr.New(apierr.NotFound, "Requested entity was not found."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": rs.Name, "createTime": rfc3339(rs.CreateTime), "source": source(rs)})
}
