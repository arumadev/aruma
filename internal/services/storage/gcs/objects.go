package gcs

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The objects of the JSON API (B §4.1-4.5, §10.1): get, list, patch, delete and rewriteTo.
// The caller is an admin of the bucket's project (routeJSON), so core's rules do not apply.

// getObject is objects.get: Node's getMetadata and exists() (a 404 is false), Go's Attrs.
// ?generation= is the current generation or not found (ADR-023 §3-10), and the preconditions
// hold on the object read, or 412.
func (a *API) getObject(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	q := r.URL.Query()
	gen, err := generation(q)
	if err != nil {
		a.writeError(w, err, b.Name, name)
		return
	}
	pre, err := preconditions(q)
	if err != nil {
		a.writeError(w, err, b.Name, name)
		return
	}
	o, err := a.svc.Stat(r.Context(), b, name, p, core.SurfaceGCS, gen)
	if err == nil && !holds(o, pre) {
		err = core.ErrPreconditionFailed
	}
	if err != nil {
		a.writeError(w, err, b.Name, name)
		return
	}
	a.writeJSON(w, http.StatusOK, a.object(o))
}

// listObjects is objects.list: Node's getFiles, Go's Objects (B §4.2). Go sends every parameter
// on every page, empty or false; an empty one is absent. What v0.3 does not list (versions,
// soft-deleted objects, folders, trailing delimiters, matchGlob) is refused 400 when asked
// for, rather than silently ignored (ADR-022 §13, B §10.3).
func (a *API) listObjects(w http.ResponseWriter, r *http.Request, b core.Bucket, p core.Principal) {
	q := r.URL.Query()
	for _, f := range []string{"versions", "includeTrailingDelimiter", "includeFoldersAsPrefixes", "softDeleted"} {
		on, err := flag(q, f)
		if err == nil && on {
			err = invalidf("%s=true is not supported by Aruma.", f)
		}
		if err != nil {
			a.writeError(w, err, b.Name, "")
			return
		}
	}
	if q.Get("matchGlob") != "" {
		a.writeError(w, invalidf("matchGlob is not supported by Aruma."), b.Name, "")
		return
	}
	size := 0
	if v := q.Get("maxResults"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			a.writeError(w, invalidf("Invalid value for maxResults: %q.", v), b.Name, "")
			return
		}
		size = n // 0 is the default page; over list_max_results is cut to it (core)
	}
	page, err := a.svc.List(r.Context(), b, p, core.ListOptions{
		Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), PageToken: q.Get("pageToken"),
		StartOffset: q.Get("startOffset"), EndOffset: q.Get("endOffset"), MaxResults: size, Surface: core.SurfaceGCS,
	})
	if err != nil {
		a.writeError(w, err, b.Name, "")
		return
	}
	res := wire.GCSObjects{Kind: wire.KindObjects, NextPageToken: page.NextPageToken, Prefixes: page.Prefixes}
	for _, o := range page.Objects {
		res.Items = append(res.Items, a.object(o))
	}
	a.writeJSON(w, http.StatusOK, res)
}

// patchObject is objects.patch: Node's setMetadata, Go's Update (B §4.3). A null field is
// deleted, metadata merges key by key and "metadata": null clears it, and the metageneration
// goes up. metadata.firebaseStorageDownloadTokens sets the download tokens (ADR-022 §9).
func (a *API) patchObject(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	body, err := a.readJSON(r)
	if err != nil {
		a.writeError(w, err, b.Name, name)
		return
	}
	attrs, err := decodeAttrs(body)
	if err != nil {
		a.writeError(w, err, b.Name, name)
		return
	}
	if q := r.URL.Query(); q.Get("predefinedAcl") != "" {
		a.writeError(w, errDeferred, b.Name, name)
		return
	}
	var o core.Object
	err = a.pinned(r, b, name, p, func(pre core.Preconditions) error {
		var err error
		o, err = a.svc.Update(r.Context(), b, name, p, core.SurfaceGCS, attrs.Patch, pre)
		return err
	})
	if err != nil {
		a.writeError(w, err, b.Name, name)
		return
	}
	a.writeJSON(w, http.StatusOK, a.object(o))
}

// deleteObject is objects.delete: 204 without a body (B §4.4). Node's ignoreNotFound turns the
// 404 into success itself.
func (a *API) deleteObject(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	err := a.pinned(r, b, name, p, func(pre core.Preconditions) error {
		return a.svc.Delete(r.Context(), b, name, p, core.SurfaceGCS, pre)
	})
	if err != nil {
		a.writeError(w, err, b.Name, name)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pinned runs a change with the request's preconditions, and with its ?generation=, which
// v0.3 serves for the current generation alone (ADR-023 §3-10): it becomes ifGenerationMatch,
// checked on the row core locks, unless the caller set that too. A precondition that fails
// while the generation asked for is not the current one is that generation not found: 404, not
// 412.
func (a *API) pinned(r *http.Request, b core.Bucket, name string, p core.Principal, change func(core.Preconditions) error) error {
	q := r.URL.Query()
	gen, err := generation(q)
	if err != nil {
		return err
	}
	pre, err := preconditions(q)
	if err != nil {
		return err
	}
	switch {
	case gen == 0:
	case pre.IfGenerationMatch == nil:
		pre.IfGenerationMatch = new(gen)
	case *pre.IfGenerationMatch != gen:
		// The generation asked for and the one it must match differ: whichever is current, one
		// of them fails.
		if _, err := a.svc.Stat(r.Context(), b, name, p, core.SurfaceGCS, gen); err != nil {
			return err
		}
		return core.ErrPreconditionFailed
	}
	err = change(pre)
	if gen != 0 && errors.Is(err, core.ErrPreconditionFailed) {
		if _, serr := a.svc.Stat(r.Context(), b, name, p, core.SurfaceGCS, gen); errors.Is(serr, core.ErrNotFound) {
			return core.ErrNotFound
		}
	}
	return err
}

// rewrite is objects.rewrite, POST .../o/<source>/rewriteTo/b/<bucket>/o/<destination>: Node's
// copy and move, Go's CopierFrom (B §4.5). Aruma copies in this one request and answers
// done:true with no rewriteToken (ADR-022 §14), so Node, which loops while a token comes back,
// and Go, which loops until done, both stop at once. The destination is in the source's
// project: another project's bucket is 403 (ADR-022 §3). A body that sets metadata replaces the
// destination's; an empty one ({}, as Node sends) copies the source's.
//
// sourceGeneration and the ifSource preconditions are checked on the source as read before the
// copy: core.Rewrite takes no source generation, so a source replaced between the two is copied
// as it is then.
func (a *API) rewrite(w http.ResponseWriter, r *http.Request, b core.Bucket, name, dest string, p core.Principal) {
	q := r.URL.Query()
	path, err := wire.SplitObjectPath(dest)
	switch {
	case err != nil:
		a.writeError(w, invalidf("The rewrite's destination path is malformed."), b.Name, name)
		return
	case !path.Objects || path.Object == "" || path.Rest != "":
		a.writeError(w, errNotFoundPath, b.Name, name)
		return
	}
	if q.Get("destinationKmsKeyName") != "" || q.Get("destinationPredefinedAcl") != "" {
		a.writeError(w, errDeferred, b.Name, name)
		return
	}
	if q.Get("rewriteToken") != "" {
		// Aruma issues none (every rewrite is done in one call), so a token is not one of ours.
		a.writeError(w, invalidf("Invalid rewriteToken."), b.Name, name)
		return
	}
	dst, err := a.svc.Bucket(r.Context(), path.Bucket)
	if err != nil {
		a.writeError(w, err, path.Bucket, path.Object)
		return
	}
	if dst.Project != b.Project {
		a.writeError(w, forbidden(p.ServiceAccount, permObjectsCreate), dst.Name, path.Object)
		return
	}
	pre, err := preconditions(q)
	if err != nil {
		a.writeError(w, err, dst.Name, path.Object)
		return
	}
	if err := a.checkSource(r.Context(), q, b, name, p); err != nil {
		a.writeError(w, err, b.Name, name)
		return
	}
	body, err := a.readJSON(r)
	if err != nil {
		a.writeError(w, err, dst.Name, path.Object)
		return
	}
	attrs, err := decodeAttrs(body)
	if err != nil {
		a.writeError(w, err, dst.Name, path.Object)
		return
	}
	var patch *core.Patch
	if attrs.Set {
		patch = &attrs.Patch
	}
	o, err := a.svc.Rewrite(r.Context(), b, name, dst, path.Object, p, patch, pre)
	if err != nil {
		bucket, object := dst.Name, path.Object
		if errors.Is(err, core.ErrNotFound) {
			bucket, object = b.Name, name // the source is what was not found
		}
		a.writeError(w, err, bucket, object)
		return
	}
	res := a.object(o)
	a.writeJSON(w, http.StatusOK, wire.GCSRewrite{Kind: wire.KindRewrite, TotalBytesRewritten: wire.Int(o.Size),
		ObjectSize: wire.Int(o.Size), Done: true, Resource: &res})
}

// checkSource checks a rewrite's sourceGeneration and ifSource preconditions on the source.
func (a *API) checkSource(ctx context.Context, q url.Values, b core.Bucket, name string, p core.Principal) error {
	gen, err := positive(q, "sourceGeneration")
	if err != nil {
		return err
	}
	pre, err := sourcePreconditions(q)
	if err != nil {
		return err
	}
	if gen == 0 && pre == (core.Preconditions{}) {
		return nil
	}
	o, err := a.svc.Stat(ctx, b, name, p, core.SurfaceGCS, gen)
	if err != nil {
		return err
	}
	if !holds(o, pre) {
		return core.ErrPreconditionFailed
	}
	return nil
}

// holds reports whether GCS's preconditions hold on an object that exists: ifGenerationMatch=0
// ("must not exist") fails on any (B §10.2-8).
func holds(o core.Object, pre core.Preconditions) bool {
	return (pre.IfGenerationMatch == nil || *pre.IfGenerationMatch == o.Generation) &&
		(pre.IfGenerationNotMatch == nil || *pre.IfGenerationNotMatch != o.Generation) &&
		(pre.IfMetagenerationMatch == nil || *pre.IfMetagenerationMatch == o.Metageneration) &&
		(pre.IfMetagenerationNotMatch == nil || *pre.IfMetagenerationNotMatch != o.Metageneration)
}
