package gcs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The bucket of the JSON API (B §4.6, ADR-022 §14): buckets.get, for Node's bucket.exists and
// getMetadata and Go's Bucket.Attrs, and buckets.patch for its cors[] alone, which govern
// alt=media (ADR-022 §11). A project has one bucket, made with the project (ADR-022 §2), so
// nothing creates or deletes one.

// getBucket is buckets.get. ifMetagenerationMatch and ifMetagenerationNotMatch hold on the
// bucket's metageneration, or 412.
func (a *API) getBucket(w http.ResponseWriter, r *http.Request, b core.Bucket, p core.Principal) {
	pre, err := bucketPreconditions(r.URL.Query())
	if err != nil {
		a.writeError(w, err, b.Name, "")
		return
	}
	info, err := a.svc.BucketInfo(r.Context(), b)
	if err == nil && !holdsBucket(info, pre) {
		err = core.ErrPreconditionFailed
	}
	if err != nil {
		a.writeError(w, err, b.Name, "")
		return
	}
	a.writeJSON(w, http.StatusOK, a.bucket(info))
}

// patchBucket is buckets.patch with a body of cors alone: Node's setCorsConfiguration and
// setMetadata({cors}), Go's Update with BucketAttrsToUpdate.CORS. Any other field is 400: v0.3
// keeps no other bucket setting, and a field ignored would look applied. "cors": null or []
// removes every rule; a body without cors changes nothing. The metageneration preconditions
// are checked by core on the settings row it locks (core.BucketConditions), so of two changes
// with the same ifMetagenerationMatch one is 412.
func (a *API) patchBucket(w http.ResponseWriter, r *http.Request, b core.Bucket, p core.Principal) {
	q := r.URL.Query()
	if q.Get("predefinedAcl") != "" || q.Get("predefinedDefaultObjectAcl") != "" {
		a.writeError(w, errDeferred, b.Name, "")
		return
	}
	pre, err := bucketPreconditions(q)
	if err != nil {
		a.writeError(w, err, b.Name, "")
		return
	}
	body, err := a.readJSON(r)
	if err != nil {
		a.writeError(w, err, b.Name, "")
		return
	}
	rules, set, err := decodeBucketPatch(body)
	if err != nil {
		a.writeError(w, err, b.Name, "")
		return
	}
	cond := core.BucketConditions{IfMetagenerationMatch: pre.IfMetagenerationMatch, IfMetagenerationNotMatch: pre.IfMetagenerationNotMatch}
	var info core.BucketInfo
	if set {
		info, err = a.svc.SetCORS(r.Context(), b, p, rules, cond)
	} else {
		// Nothing changes: the conditions hold on the bucket as it is.
		if info, err = a.svc.BucketInfo(r.Context(), b); err == nil && !holdsBucket(info, pre) {
			err = core.ErrPreconditionFailed
		}
	}
	if err != nil {
		a.writeError(w, err, b.Name, "")
		return
	}
	a.writeJSON(w, http.StatusOK, a.bucket(info))
}

// decodeBucketPatch reads a buckets.patch body: its cors rules, and whether it sets them.
func decodeBucketPatch(data []byte) (rules []core.CORSRule, set bool, err error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, false, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, false, invalidf("Invalid JSON payload: the body must be an object.")
	}
	for k := range body {
		if k != "cors" {
			return nil, false, invalidf("Aruma patches a bucket's cors alone, not %q.", k)
		}
	}
	v, ok := body["cors"]
	if !ok {
		return nil, false, nil
	}
	rules = []core.CORSRule{}
	if isNull(v) {
		return rules, true, nil
	}
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	var in []wire.GCSCORS
	if err := dec.Decode(&in); err != nil {
		return nil, false, invalidf("Invalid value for cors: a list of rules with origin, method, responseHeader and maxAgeSeconds.")
	}
	for _, c := range in {
		rules = append(rules, core.CORSRule{Origins: slices.Clone(c.Origin), Methods: slices.Clone(c.Method),
			ResponseHeaders: slices.Clone(c.ResponseHeader), MaxAgeSeconds: c.MaxAgeSeconds})
	}
	return rules, true, nil
}

// bucketPreconditions reads a bucket's ifMetagenerationMatch and ifMetagenerationNotMatch. A
// bucket has no generation to match: ifGenerationMatch is 400, as GCS has no such parameter.
func bucketPreconditions(q url.Values) (core.Preconditions, error) {
	pre, err := preconditions(q)
	if err != nil {
		return pre, err
	}
	if pre.IfGenerationMatch != nil || pre.IfGenerationNotMatch != nil {
		return pre, invalidf("A bucket has no generation precondition.")
	}
	return pre, nil
}

func holdsBucket(info core.BucketInfo, pre core.Preconditions) bool {
	return (pre.IfMetagenerationMatch == nil || *pre.IfMetagenerationMatch == info.Metageneration) &&
		(pre.IfMetagenerationNotMatch == nil || *pre.IfMetagenerationNotMatch != info.Metageneration)
}
