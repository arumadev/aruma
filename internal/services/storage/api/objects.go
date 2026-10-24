package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// The object operations of /v0 other than the bytes (A §3.2, §3.5, §3.6, §7; E §5): the
// metadata read, which mints a download token for an object without one, the metadata change,
// the delete and the admin's token calls. Every reply but the delete's is the object's resource.

// metadata serves GET /o/{object}: the object's resource, after the rules allow a get. An object
// without a download token gets one first, so getDownloadURL works for a file an Admin uploaded
// (core.Service.Stat, ADR-022 §9). The token in ?token= opens the bytes alone, never this
// (ADR-023 §3-18): here it is ignored, and the rules decide.
func (a *API) metadata(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	o, err := a.svc.Stat(r.Context(), b, name, p, core.SurfaceFirebase, 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeObject(w, http.StatusOK, o)
}

// update serves PATCH /o/{object}, and Android's PUT with X-HTTP-Method-Override: PATCH: a
// metadata change after the rules allow an update. The answer is the whole resource.
func (a *API) update(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	patch, err := a.readPatch(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	o, err := a.svc.Update(r.Context(), b, name, p, core.SurfaceFirebase, patch, core.Preconditions{})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeObject(w, http.StatusOK, o)
}

// patchFields are the content fields a /v0 PATCH may change, by their JSON names.
var patchFields = []string{core.FieldContentType, core.FieldCacheControl, core.FieldContentDisposition,
	core.FieldContentEncoding, core.FieldContentLanguage}

// readPatch reads a PATCH body, a JSON object under json_body_bytes, as A §7 and ADR-022 §9 say:
//
//   - The five content fields: a string sets the field, null deletes it.
//   - metadata: an object merges key by key, a null value deleting its key; metadata: null
//     clears the custom metadata (ADR-023 §3-17). firebaseStorageDownloadTokens is dropped
//     from it, with a value or null, before the rules see it: a client neither plants nor
//     removes a token.
//   - Everything else is ignored, null or not: name, bucket (iOS sends ""), size, generation,
//     metageneration, the times, md5Hash, etag, crc32c, storageClass and downloadTokens, which
//     iOS sends back as null whenever it read them (A §1.10).
//
// A writable field with a value that is neither a string nor null is 400, as is a body that is
// not one JSON object. An empty body changes nothing but the metageneration.
func (a *API) readPatch(r *http.Request) (core.Patch, error) {
	limit := a.svc.Limits().JSONBodyBytes
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	switch {
	case err != nil:
		return core.Patch{}, statusError(http.StatusBadRequest, "The request body could not be read.")
	case int64(len(body)) > limit:
		return core.Patch{}, statusError(http.StatusRequestEntityTooLarge, "The request body is too large.")
	}
	var patch core.Patch
	if len(bytes.TrimSpace(body)) == 0 {
		return patch, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil || doc == nil {
		return core.Patch{}, statusError(http.StatusBadRequest, "The request body must be a JSON object.")
	}
	for _, name := range patchFields {
		raw, ok := doc[name]
		if !ok {
			continue
		}
		v, err := nullableString(raw)
		if err != nil {
			return core.Patch{}, statusError(http.StatusBadRequest, "The "+name+" must be a string or null.")
		}
		if patch.Fields == nil {
			patch.Fields = map[string]*string{}
		}
		patch.Fields[name] = v
	}
	if raw, ok := doc["metadata"]; ok {
		if isNull(raw) {
			patch.ClearMetadata = true
		} else {
			var md map[string]json.RawMessage
			if err := json.Unmarshal(raw, &md); err != nil || md == nil {
				return core.Patch{}, statusError(http.StatusBadRequest, "The metadata must be an object or null.")
			}
			patch.Metadata = make(map[string]*string, len(md))
			for k, raw := range md {
				v, err := nullableString(raw)
				if err != nil {
					return core.Patch{}, statusError(http.StatusBadRequest, "A custom metadata value must be a string or null.")
				}
				patch.Metadata[k] = v
			}
			core.StripClientTokens(patch.Metadata)
		}
	}
	return patch, nil
}

var errNotString = errors.New("not a string or null")

// nullableString reads a JSON string as a value and null as nil.
func nullableString(raw json.RawMessage) (*string, error) {
	if isNull(raw) {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, errNotString
	}
	return &s, nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// delete serves DELETE /o/{object}: 204 without a body once the rules allow a delete, as the
// emulator answers and every SDK accepts (A §3.6).
func (a *API) delete(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	if err := a.svc.Delete(r.Context(), b, name, p, core.SurfaceFirebase, core.Preconditions{}); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// tokens serves POST /o/{object}?create_token=true and ?delete_token=<t>, the console's and the
// Admin's token calls: admins alone, others 403 (core.Service.CreateToken, DeleteToken). A
// create_token other than true is 400 whoever asks, as the emulator checks it first
// (tokens.json, CT). With both, create_token is the call (the emulator answers one change).
// Deleting the last token makes a new one at once (E §5).
func (a *API) tokens(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	q := r.URL.Query()
	var (
		o   core.Object
		err error
	)
	if q.Has("create_token") {
		if q.Get("create_token") != "true" {
			a.writeError(w, r, statusError(http.StatusBadRequest, "create_token must be true."))
			return
		}
		o, err = a.svc.CreateToken(r.Context(), b, name, p)
	} else {
		o, err = a.svc.DeleteToken(r.Context(), b, name, p, q.Get("delete_token"))
	}
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeObject(w, http.StatusOK, o)
}
