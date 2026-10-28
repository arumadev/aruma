package mgmt

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// The firebaserules API (firebaserules.googleapis.com/v1), as firebase deploy and init send it
// for Storage (C §1.5, R1 to R8, ADR-022 §15), over tenancy's rulesets and releases. The CLI
// reaches it through FIREBASE_RULES_URL. Every success is 200 exactly, deletion included,
// because the CLI checks response.status === 200 and turns any other 2xx into "Unexpected error
// encountered with rules." The API does not care which service a ruleset is for: it takes
// cloud.firestore as well, which nothing reads before v0.4.

// rulesRoutes registers the firebaserules v1 API (C §1.5, R1 to R8) under /v1/projects/: :test,
// rulesets and releases, over tenancy, with Options.RulesLimits.
func (a *API) rulesRoutes(r *httpx.Router) {
	h := func(method, pattern string, fn http.HandlerFunc) {
		r.Handle(method, "/v1/projects/{project}"+pattern, a.auth.Require(policy, fn))
	}
	h("POST", ":test", a.testRuleset)                    // R1
	h("GET", "/releases", a.listReleases)                // R2
	h("GET", "/rulesets/{ruleset}", a.getRuleset)        // R3
	h("POST", "/rulesets", a.createRuleset)              // R4
	h("PATCH", "/releases/{release...}", a.patchRelease) // R5
	h("POST", "/releases", a.createRelease)              // R6
	h("GET", "/rulesets", a.listRulesets)                // R7
	h("DELETE", "/rulesets/{ruleset}", a.deleteRuleset)  // R8
	h("GET", "/releases/{release...}", a.getRelease)
}

type rulesSource struct {
	Files []rulesFile `json:"files"`
}

type rulesFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type rulesetResource struct {
	Name       string       `json:"name"`
	Source     *rulesSource `json:"source,omitempty"` // left out of listings, as in production
	CreateTime string       `json:"createTime"`
}

type releaseResource struct {
	Name        string `json:"name"`
	RulesetName string `json:"rulesetName"`
	CreateTime  string `json:"createTime"`
	UpdateTime  string `json:"updateTime"`
}

// issue is one entry of issues[], as TestRulesetResponse carries it: a position (the file's name
// alone for an issue about the whole file), the text, and ERROR or WARNING.
type issue struct {
	SourcePosition sourcePosition `json:"sourcePosition"`
	Description    string         `json:"description"`
	Severity       string         `json:"severity"`
}

// sourcePosition leaves zeros out, as proto3 JSON does: an issue at the file's first character
// has no currentOffset (production/raw/limits.json).
type sourcePosition struct {
	FileName      string `json:"fileName,omitempty"`
	Line          int    `json:"line,omitzero"`
	Column        int    `json:"column,omitzero"`
	CurrentOffset int    `json:"currentOffset,omitzero"`
	EndOffset     int    `json:"endOffset,omitzero"`
}

// rulesTime writes a time as the Rules API does, RFC 3339 in UTC, with a fixed six fractional
// digits (Postgres keeps microseconds): firebase-tools sorts releases and rulesets by comparing
// createTime as text (gcp/rules.ts:95,161), which a fixed width keeps in time order.
func rulesTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

func toRuleset(rs core.Ruleset) rulesetResource {
	out := rulesetResource{Name: rs.Name, CreateTime: rulesTime(rs.CreateTime)}
	if rs.Files != nil {
		out.Source = &rulesSource{Files: make([]rulesFile, 0, len(rs.Files))}
		for _, f := range rs.Files {
			out.Source.Files = append(out.Source.Files, rulesFile{Name: f.Name, Content: f.Content})
		}
	}
	return out
}

func toRelease(rel core.Release) releaseResource {
	return releaseResource{Name: rel.Name, RulesetName: rel.RulesetName,
		CreateTime: rulesTime(rel.CreateTime), UpdateTime: rulesTime(rel.UpdateTime)}
}

func toIssues(is rules.Issues) []issue {
	out := make([]issue, 0, len(is))
	for _, i := range is {
		pos := sourcePosition{FileName: i.File}
		if i.Position != nil {
			pos.Line, pos.Column = i.Position.Line, i.Position.Column
			pos.CurrentOffset, pos.EndOffset = i.Position.CurrentOffset, i.Position.EndOffset
		}
		out = append(out, issue{SourcePosition: pos, Description: i.Description, Severity: i.Severity.String()})
	}
	return out
}

// oneFile is the one file a source must hold: the Rules API takes no more ("Currently only one
// file source can be passed to firebase rules.", ADR-021 §2.1), and an empty source has nothing
// to compile.
func oneFile(src *rulesSource) (rules.File, error) {
	if src == nil || len(src.Files) == 0 {
		return rules.File{}, apierr.Invalid("source", "A ruleset needs a source with one file.")
	}
	if len(src.Files) != 1 {
		return rules.File{}, apierr.New(apierr.InvalidArgument, "Currently only one file source can be passed to firebase rules.")
	}
	return rules.File{Name: src.Files[0].Name, Content: src.Files[0].Content}, nil
}

// invalidServiceFirestore is the engine's error on a Firestore file compiled for Storage
// (internal/platform/rules/check.go).
const invalidServiceFirestore = "Invalid service name: " + string(rules.Firestore) + "."

// compile compiles a file for the service its declaration names, as the Rules API does: Storage,
// or Firestore for a file that declares cloud.firestore. A name that is neither compiles for
// Storage with the engine's warning, and grants nothing. tenancy compiles it, in a turn of the
// process's compilations (core.Service.CompileRules); the error is the request's end while it
// waited.
func (a *API) compile(ctx context.Context, f rules.File) (*rules.Ruleset, rules.Issues, error) {
	rs, issues, err := a.svc.CompileRules(ctx, rules.Storage, f)
	if err == nil && declaresFirestore(issues) {
		rs, issues, err = a.svc.CompileRules(ctx, rules.Firestore, f)
	}
	return rs, issues, err
}

// declaresFirestore reports whether a compilation for Storage failed because the file declares
// cloud.firestore.
func declaresFirestore(issues rules.Issues) bool {
	return slices.ContainsFunc(issues, func(i rules.Issue) bool {
		return i.Severity == rules.SeverityError && i.Description == invalidServiceFirestore
	})
}

// tooLarge is the one compile error the Rules API answers 400 instead of listing it: a source
// past the size limit, "Total size of sources must be smaller than 262144."
// (production/raw/followups.json, size_comment_262120).
func tooLarge(issues rules.Issues) error {
	for _, i := range issues {
		if i.Position == nil && i.Severity == rules.SeverityError && strings.HasPrefix(i.Description, "Total size of sources must be smaller than ") {
			return apierr.New(apierr.InvalidArgument, "%s", i.Description)
		}
	}
	return nil
}

// decodeSource reads a request that carries a rules source. A body past the API's 1 MiB cap
// (httpx.MaxBodyBytes) holds a source past the size limit, and gets the Rules API's answer for
// one (production/raw/limits.json, size_comment_1048576) rather than the generic 413.
func (a *API) decodeSource(r *http.Request, v any) error {
	err := httpx.DecodeJSON(r, v, false)
	if err != nil && apierr.From(err).HTTPStatus() == http.StatusRequestEntityTooLarge {
		return apierr.New(apierr.InvalidArgument, "Total size of sources must be smaller than %d.", a.opts.RulesLimits.SourceBytes)
	}
	return err
}

// R1. A source that does not compile is 200 with its issues, as production answers
// (production/raw/limits.json, call_depth_22), so firebase deploy prints "[E] line:column -
// description" and stops; a source that compiles is 200 with its warnings, or {}. Test suites
// are not run: a request with test cases is refused rather than answered as if they passed
// (docs/reference/compatibility.md).
func (a *API) testRuleset(w http.ResponseWriter, r *http.Request) {
	if _, err := a.activeProject(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Source    *rulesSource `json:"source"`
		TestSuite *struct {
			TestCases []any `json:"testCases"`
		} `json:"testSuite"`
	}
	if err := a.decodeSource(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	f, err := oneFile(in.Source)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rs, issues, err := a.compile(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := tooLarge(issues); err != nil {
		a.fail(w, r, err)
		return
	}
	if rs != nil && in.TestSuite != nil && len(in.TestSuite.TestCases) > 0 {
		a.fail(w, r, apierr.Invalid("testSuite", "Aruma's Rules API compiles a source; it does not run test cases."))
		return
	}
	body := map[string]any{}
	if len(issues) > 0 {
		body["issues"] = toIssues(issues)
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// R4. The ruleset is for the service its source declares. A source that does not compile is
// INVALID_ARGUMENT with its errors in the message (firebase deploy runs :test first, so it only
// sees this when the source changed in between), and the project's ruleset limit is
// RESOURCE_EXHAUSTED (429), on which the CLI offers to delete old rulesets (C §1.5).
func (a *API) createRuleset(w http.ResponseWriter, r *http.Request) {
	if _, err := a.activeProject(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Source *rulesSource `json:"source"`
		// A named Firestore database's (C §4), sent in snake_case by firebase-tools.
		AttachmentPoint      string `json:"attachment_point"`
		AttachmentPointCamel string `json:"attachmentPoint"`
	}
	if err := a.decodeSource(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.AttachmentPoint != "" || in.AttachmentPointCamel != "" {
		// Named Firestore databases come after v0.4 (ADR-023): no release could use it.
		a.fail(w, r, apierr.Invalid("attachmentPoint", "Rulesets for named Firestore databases are not supported."))
		return
	}
	f, err := oneFile(in.Source)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// tenancy compiles the source, once: for Storage, and again for Firestore only when the file
	// declares cloud.firestore, each in a turn of the process's compilations.
	create := func(svc rules.Service) (core.Ruleset, rules.Issues, error) {
		return a.svc.CreateRuleset(r.Context(), principal(r), r.PathValue("project"), svc, []rules.File{f})
	}
	rs, issues, err := create(rules.Storage)
	if err != nil && declaresFirestore(issues) {
		rs, issues, err = create(rules.Firestore)
	}
	if err != nil {
		if sized := tooLarge(issues); sized != nil {
			err = sized
		} else if e := apierr.From(err); e.Code == apierr.InvalidArgument && issues.Err() != nil {
			err = apierr.New(apierr.InvalidArgument, "%s\n%s", e.Message, errorLines(issues))
		}
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toRuleset(rs))
}

// errorLines lists the errors among issues as firebase-tools prints them.
func errorLines(issues rules.Issues) string {
	var lines []string
	for _, i := range issues {
		if i.Severity == rules.SeverityError {
			lines = append(lines, i.String())
		}
	}
	return strings.Join(lines, "\n")
}

// R3
func (a *API) getRuleset(w http.ResponseWriter, r *http.Request) {
	rs, err := a.svc.GetRuleset(r.Context(), principal(r), r.PathValue("project"), r.PathValue("ruleset"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toRuleset(rs))
}

// R7. Names and times only; the source is read with R3.
func (a *API) listRulesets(w http.ResponseWriter, r *http.Request) {
	opt, err := rulesListOptions(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page, err := a.svc.ListRulesets(r.Context(), principal(r), r.PathValue("project"), opt)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]rulesetResource, 0, len(page.Items))
	for _, rs := range page.Items {
		out = append(out, toRuleset(rs))
	}
	writePage(w, "rulesets", out, page.NextPageToken)
}

// R8: 200 with {}. A ruleset a release points to is FAILED_PRECONDITION.
func (a *API) deleteRuleset(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteRuleset(r.Context(), principal(r), r.PathValue("project"), r.PathValue("ruleset")); err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct{}{})
}

// R2. The Storage release is named after the project's bucket, so firebase-tools finds it by its
// prefix projects/{p}/releases/firebase.storage/<bucket> (ADR-022 §2).
func (a *API) listReleases(w http.ResponseWriter, r *http.Request) {
	opt, err := rulesListOptions(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page, err := a.svc.ListReleases(r.Context(), principal(r), r.PathValue("project"), opt)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]releaseResource, 0, len(page.Items))
	for _, rel := range page.Items {
		out = append(out, toRelease(rel))
	}
	writePage(w, "releases", out, page.NextPageToken)
}

// rulesListOptions reads the paging of R2 and R7. The first page comes with pageToken= empty
// (C §1.5). A filter is refused rather than ignored: a caller that deletes what a filtered
// listing returns must not get everything.
func rulesListOptions(r *http.Request) (core.ListOptions, error) {
	if r.URL.Query().Get("filter") != "" {
		return core.ListOptions{}, apierr.Invalid("filter", "Filtering rulesets and releases is not supported.")
	}
	opt, err := listOptions(r)
	opt.ShowDeleted = false
	return opt, err
}

// writePage writes a listing as proto3 JSON does: an empty one is {}, and nextPageToken is there
// only when another page follows. firebase-tools reads both forms (gcp/rules.ts:89,152).
func writePage[T any](w http.ResponseWriter, field string, items []T, next string) {
	body := map[string]any{}
	if len(items) > 0 {
		body[field] = items
	}
	if next != "" {
		body["nextPageToken"] = next
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (a *API) getRelease(w http.ResponseWriter, r *http.Request) {
	rel, err := a.svc.GetRelease(r.Context(), principal(r), r.PathValue("project"), r.PathValue("release"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toRelease(rel))
}

// R5. firebase-tools sends {"release": {"name", "rulesetName"}} without an updateMask, and
// creates the release with R6 when this fails, so a release that does not exist is NOT_FOUND
// here (C §1.5).
func (a *API) patchRelease(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Release *struct {
			Name        string `json:"name"`
			RulesetName string `json:"rulesetName"`
		} `json:"release"`
		UpdateMask string `json:"updateMask"`
	}
	if err := httpx.DecodeJSON(r, &in, false); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.activeProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	release := r.PathValue("release")
	switch {
	case in.Release == nil || in.Release.RulesetName == "":
		err = apierr.Invalid("release.rulesetName", "A release needs a rulesetName.")
	case in.Release.Name != "":
		var named string
		if named, err = releaseID(p, in.Release.Name); err == nil && named != release {
			err = apierr.Invalid("release.name", "The release's name does not match the request's.")
		}
	}
	if err == nil {
		err = releaseMask(cmp.Or(in.UpdateMask, param(r, "updateMask", "update_mask")))
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if _, err := a.svc.GetRelease(r.Context(), principal(r), p.ID, release); err != nil {
		a.fail(w, r, err)
		return
	}
	rel, err := a.svc.PublishRelease(r.Context(), principal(r), p.ID, release, in.Release.RulesetName)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toRelease(rel))
}

// releaseMask accepts the one field a release update changes.
func releaseMask(mask string) error {
	for f := range strings.SplitSeq(mask, ",") {
		if f = strings.TrimSpace(f); f != "" && f != "rulesetName" && f != "ruleset_name" {
			return apierr.Invalid("updateMask", "Field "+f+" cannot be updated.")
		}
	}
	return nil
}

// R6. A release that exists is ALREADY_EXISTS; firebase-tools updates with R5 first.
func (a *API) createRelease(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		RulesetName string `json:"rulesetName"`
	}
	if err := httpx.DecodeJSON(r, &in, false); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.activeProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	release, err := releaseID(p, in.Name)
	if err == nil && in.RulesetName == "" {
		err = apierr.Invalid("rulesetName", "A release needs a rulesetName.")
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	switch _, err := a.svc.GetRelease(r.Context(), principal(r), p.ID, release); {
	case err == nil:
		a.fail(w, r, apierr.New(apierr.AlreadyExists, "Release %s already exists.", in.Name))
		return
	case apierr.CodeOf(err) != apierr.NotFound:
		a.fail(w, r, err)
		return
	}
	rel, err := a.svc.PublishRelease(r.Context(), principal(r), p.ID, release, in.RulesetName)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toRelease(rel))
}

// releaseID reads a release's full name, projects/{project}/releases/{release}, under the
// request's project by its id or number. tenancy checks the release part.
func releaseID(p core.Project, name string) (string, error) {
	rest, ok := strings.CutPrefix(name, "projects/")
	proj, release, cut := strings.Cut(rest, "/releases/")
	if !ok || !cut || release == "" {
		return "", apierr.Invalid("name", "A release's name is projects/{project}/releases/{release}.")
	}
	if proj != p.ID && proj != strconv.FormatInt(p.Number, 10) {
		return "", apierr.Invalid("name", "The release's name is not under projects/"+p.ID+".")
	}
	return release, nil
}
