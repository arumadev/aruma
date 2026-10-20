package console

import (
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/services/rtdb/core"
)

// dataRoutes registers the data browser's routes. Every one takes the node's path in ?path=,
// "/" or empty for the root, as the SDKs write paths.
func (h *handlers) dataRoutes() {
	const base = "/console/v1/projects/{project}/database/data"
	h.m.Handle(anyCred, "GET", base, h.browse)
	h.m.Handle(anyCred, "GET", base+":export", h.export)
	h.m.Handle(anyCred, "PUT", base, h.setData)
	h.m.Handle(anyCred, "PATCH", base, h.updateData)
	h.m.Handle(anyCred, "DELETE", base, h.deleteData)
}

const (
	// defaultPage and the core's MaxShallowPage bound the children of one page.
	defaultPage = 100
	// previewBytes caps a string leaf shown among its siblings; the full value is one read of
	// the leaf's own path away.
	previewBytes = 1000
	// maxWriteBody is the console's body cap: a larger import belongs to the REST API.
	maxWriteBody = httpx.MaxBodyBytes
	// maxExportBytes caps the value bytes one export reads into memory.
	maxExportBytes = 16 << 20
)

// maxExportRows caps the values one export reads into memory (a variable for the tests).
var maxExportRows = 200_000

// childJSON is one child in a page: a leaf's value, or hasChildren for an object.
type childJSON struct {
	Key         string          `json:"key"`
	Value       json.RawMessage `json:"value,omitempty"`
	Truncated   bool            `json:"truncated,omitempty"`
	HasChildren bool            `json:"hasChildren,omitempty"`
}

// nodePath is the ?path= of a request, as given.
func nodePath(r *http.Request) string { return r.URL.Query().Get("path") }

// displayPath is p as the dashboard shows it, from the root: "/", "/users/amal".
func displayPath(p string) string {
	parsed, err := core.ParsePath(p)
	if err != nil {
		return p
	}
	return "/" + parsed.String()
}

// browse returns one page of the node at ?path=, as REST's shallow=true does, with the values
// of the children that are leaves. pageSize defaults to 100 and is at most 1000; pageToken
// continues after the last child of the previous page.
func (h *handlers) browse(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("pageSize"))
	if size <= 0 {
		size = defaultPage
	}
	var after string
	if tok := q.Get("pageToken"); tok != "" {
		b, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil || len(b) == 0 {
			h.m.Fail(w, r, apierr.Invalid("pageToken", "The page token is not valid."))
			return
		}
		after = string(b)
	}
	page, err := h.svc.Shallow(r.Context(), t, nodePath(r), after, size)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := map[string]any{
		"path":          displayPath(nodePath(r)),
		"exists":        page.Value != nil || len(page.Children) > 0 || after != "",
		"children":      []childJSON{},
		"nextPageToken": "",
	}
	if page.Value != nil {
		out["value"] = json.RawMessage(page.Value.JSON())
	}
	children := make([]childJSON, 0, len(page.Children))
	for _, c := range page.Children {
		if c.Leaf == nil {
			children = append(children, childJSON{Key: c.Key, HasChildren: true})
			continue
		}
		v, truncated := preview(c.Leaf)
		children = append(children, childJSON{Key: c.Key, Value: v, Truncated: truncated})
	}
	out["children"] = children
	if page.Next != "" {
		out["nextPageToken"] = base64.RawURLEncoding.EncodeToString([]byte(page.Next))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// preview is a leaf's JSON, its string cut to previewBytes on a character boundary.
func preview(n *core.Node) (json.RawMessage, bool) {
	s := n.StringValue()
	if n.Kind() != core.KindString || len(s) <= previewBytes {
		return n.JSON(), false
	}
	cut := previewBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return core.String(s[:cut]).JSON(), true
}

// export sends the subtree at ?path= as a JSON file, as a client reads it (without
// priorities). It refuses a subtree over maxExportRows rows or maxExportBytes of values,
// counted before anything is loaded.
func (h *handlers) export(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	size, err := h.svc.SizeOf(r.Context(), t, nodePath(r), maxExportRows)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if size.Capped || size.Bytes > maxExportBytes {
		h.m.Fail(w, r, apierr.New(apierr.FailedPrecondition,
			"The data at this path is too large to export from the console (more than %d values or %d MiB); read it with the REST API or an Admin SDK.",
			maxExportRows, maxExportBytes>>20))
		return
	}
	var node *core.Node
	err = h.admin(t, func(s *core.Session, done func(error)) {
		s.Get(nodePath(r), nil, func(n *core.Node, err error) {
			node = n
			done(err)
		})
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	name := t.Name
	if p, _ := core.ParsePath(nodePath(r)); !p.IsRoot() {
		name += "-" + strings.Join(p, "-")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + "-export.json"}))
	_, _ = w.Write(node.JSON())
}

// admin runs fn on an admin session of the database and waits for it to call done. The
// session goes through the database's writer like an SDK's, so its writes reach listeners.
func (h *handlers) admin(t core.Target, fn func(s *core.Session, done func(error))) error {
	s, err := h.svc.Open(t, core.Auth{Admin: true, Account: "console"}, core.OpenOptions{})
	if err != nil {
		return err
	}
	defer s.Close()
	result := make(chan error, 1)
	fn(s, func(err error) { result <- err })
	return <-result
}

// writeBody is the body of a write: the value in the export format the SDKs send, so
// .priority, .value and server values such as {".sv": "timestamp"} work.
type writeBody struct {
	Value json.RawMessage `json:"value"`
}

func (h *handlers) decodeWrite(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	var in writeBody
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return nil, false
	}
	if len(in.Value) == 0 {
		h.m.Fail(w, r, apierr.Invalid("value", "A value is required; delete a node with DELETE."))
		return nil, false
	}
	return in.Value, true
}

// setData replaces the node at ?path= with the body's value, as set() does; null deletes it.
func (h *handlers) setData(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	value, ok := h.decodeWrite(w, r)
	if !ok {
		return
	}
	err = h.admin(t, func(s *core.Session, done func(error)) {
		s.Set(nodePath(r), value, nil, func(_ *core.Node, err error) { done(err) })
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateData writes each child of the body's value, an object keyed by paths relative to
// ?path=, at once, as update() does.
func (h *handlers) updateData(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	value, ok := h.decodeWrite(w, r)
	if !ok {
		return
	}
	err = h.admin(t, func(s *core.Session, done func(error)) { s.Update(nodePath(r), value, done) })
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteData removes the node at ?path= and everything below it.
func (h *handlers) deleteData(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	err = h.admin(t, func(s *core.Session, done func(error)) {
		s.Set(nodePath(r), json.RawMessage("null"), nil, func(_ *core.Node, err error) { done(err) })
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
