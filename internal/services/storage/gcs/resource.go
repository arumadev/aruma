package gcs

import (
	"bytes"
	"encoding/json"
	"maps"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// Between core's values and the GCS resources (B §7, §4.6): what this surface writes, and the
// object metadata a request body carries.

// storageClass is every object's and the bucket's class: v0.3 has one (ADR-023 §2).
const storageClass = "STANDARD"

// object is an object's resource on this surface (B §7): the numbers as strings, crc32c and
// md5Hash in base64, the links absolute from PublicURL, and the download tokens in
// metadata.firebaseStorageDownloadTokens, comma-separated, as GCS holds them (ADR-022 §9).
// Unlike /v0, an unset contentEncoding stays absent.
func (a *API) object(o core.Object) wire.GCSObject {
	created := wire.Time(o.Created)
	r := wire.GCSObject{
		Kind:                    wire.KindObject,
		ID:                      wire.GCSObjectID(o.Bucket, o.Name, o.Generation),
		SelfLink:                wire.GCSSelfLink(a.opts.PublicURL, o.Bucket, o.Name),
		MediaLink:               wire.GCSMediaLink(a.opts.PublicURL, o.Bucket, o.Name, o.Generation),
		Name:                    o.Name,
		Bucket:                  o.Bucket,
		Generation:              wire.Int(o.Generation),
		Metageneration:          wire.Int(o.Metageneration),
		ContentType:             o.ContentType,
		StorageClass:            storageClass,
		Size:                    wire.Int(o.Size),
		MD5Hash:                 wire.MD5(o.MD5),
		CRC32C:                  wire.CRC32C(o.CRC32C),
		ETag:                    wire.ETag(o.Generation, o.Metageneration),
		ContentEncoding:         o.ContentEncoding,
		ContentDisposition:      o.ContentDisposition,
		ContentLanguage:         o.ContentLanguage,
		CacheControl:            o.CacheControl,
		TimeCreated:             created,
		Updated:                 wire.Time(o.Updated),
		TimeStorageClassUpdated: created,
		TimeFinalized:           created,
	}
	if len(o.Metadata) > 0 || len(o.DownloadTokens) > 0 {
		r.Metadata = maps.Clone(o.Metadata)
		if r.Metadata == nil {
			r.Metadata = map[string]string{}
		}
		if len(o.DownloadTokens) > 0 {
			r.Metadata[core.TokensKey] = strings.Join(o.DownloadTokens, ",")
		}
	}
	return r
}

// bucket is the bucket resource of buckets.get and buckets.patch (B §4.6), in production's
// field order (bucket_probe.json). Its location is nominal: one server holds every byte.
func (a *API) bucket(info core.BucketInfo) wire.GCSBucket {
	r := wire.GCSBucket{
		Kind:           wire.KindBucket,
		SelfLink:       wire.GCSBucketSelfLink(a.opts.PublicURL, info.Name),
		ID:             info.Name,
		Name:           info.Name,
		ProjectNumber:  wire.Int(info.Project),
		Metageneration: wire.Int(info.Metageneration),
		Location:       "US",
		StorageClass:   storageClass,
		ETag:           wire.BucketETag(info.Metageneration),
		TimeCreated:    wire.Time(info.Created),
		Updated:        wire.Time(info.Updated),
		LocationType:   "multi-region",
	}
	for _, c := range info.CORS {
		r.CORS = append(r.CORS, wire.GCSCORS{Origin: c.Origins, Method: c.Methods, ResponseHeader: c.ResponseHeaders,
			MaxAgeSeconds: c.MaxAgeSeconds})
	}
	return r
}

// objectAttrs is an object's metadata as a request body writes it: a PATCH (B §4.3), a
// rewrite's destination (B §4.5), an upload's JSON (B §5.2, §5.3).
type objectAttrs struct {
	// Name is the body's "name", which a multipart upload may carry instead of the query.
	Name string
	// Patch holds the writable fields. On a PATCH a null field is deleted, "metadata" merges key
	// by key, and "metadata": null clears it (B §4.3). metadata.firebaseStorageDownloadTokens
	// becomes Patch.Tokens: an admin sets the download tokens there (ADR-022 §9), and null or ""
	// removes them.
	Patch core.Patch
	// Set reports a writable field in the body: a rewrite then replaces the destination's
	// metadata instead of copying the source's (B §4.5).
	Set bool
	// Want is what the body claims the object's checksums are (crc32c, md5Hash, B §5.8).
	Want core.Checksums
}

// contentFields are the writable content fields, by their GCS names, which are core's.
var contentFields = []string{core.FieldContentType, core.FieldContentEncoding, core.FieldContentDisposition,
	core.FieldContentLanguage, core.FieldCacheControl}

// deferredFields are object fields v0.3 does not keep (ADR-023 §2): a body that sets one to
// anything but its empty value is 501, so nobody believes it took effect.
var deferredFields = []string{"acl", "customTime", "temporaryHold", "eventBasedHold", "retention", "contexts",
	"kmsKeyName", "customerEncryption", "storageClass"}

// decodeAttrs reads an object's metadata from a JSON body. An empty body is no metadata. The
// output fields (kind, id, generation, size, timeCreated and the rest) are ignored, as a
// resource read back and sent again carries them.
func decodeAttrs(data []byte) (objectAttrs, error) {
	var attrs objectAttrs
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return attrs, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return attrs, invalidf("Invalid JSON payload: the body must be an object.")
	}
	for _, f := range deferredFields {
		if v, ok := body[f]; ok && !emptyJSON(v) && !(f == "storageClass" && string(v) == `"`+storageClass+`"`) {
			return attrs, &apiError{status: http.StatusNotImplemented, reason: wire.ReasonNotImplemented,
				message: "The object field " + f + " is not implemented by Aruma."}
		}
	}
	if v, ok := body["name"]; ok && !isNull(v) {
		if err := json.Unmarshal(v, &attrs.Name); err != nil {
			return attrs, invalidf("Invalid value for name.")
		}
	}
	for _, f := range contentFields {
		v, ok := body[f]
		if !ok {
			continue
		}
		if attrs.Patch.Fields == nil {
			attrs.Patch.Fields = map[string]*string{}
		}
		attrs.Set = true
		if isNull(v) {
			attrs.Patch.Fields[f] = nil
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return attrs, invalidf("Invalid value for %s: a string or null.", f)
		}
		attrs.Patch.Fields[f] = &s
	}
	if v, ok := body["metadata"]; ok {
		attrs.Set = true
		if err := decodeMetadata(v, &attrs.Patch); err != nil {
			return attrs, err
		}
	}
	if v, ok := body["crc32c"]; ok && !isNull(v) {
		var s string
		crc, err := uint32(0), json.Unmarshal(v, &s)
		if err == nil {
			crc, err = wire.ParseCRC32C(s)
		}
		if err != nil {
			return attrs, invalidf("Invalid value for crc32c.")
		}
		attrs.Want.CRC32C = &crc
	}
	if v, ok := body["md5Hash"]; ok && !isNull(v) {
		var s string
		var sum [16]byte
		err := json.Unmarshal(v, &s)
		if err == nil {
			sum, err = wire.ParseMD5(s)
		}
		if err != nil {
			return attrs, invalidf("Invalid value for md5Hash.")
		}
		attrs.Want.MD5 = &sum
	}
	return attrs, nil
}

// decodeMetadata reads "metadata": null (clear), or an object of strings and nulls.
func decodeMetadata(v json.RawMessage, p *core.Patch) error {
	if isNull(v) {
		p.ClearMetadata = true
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(v, &raw); err != nil || raw == nil {
		return invalidf("Invalid value for metadata: an object of strings.")
	}
	md := make(map[string]*string, len(raw))
	for k, rv := range raw {
		s, ok := metadataValue(rv)
		if !ok {
			return invalidf("Invalid value for metadata: an object of strings.")
		}
		md[k] = s
	}
	if t, ok := md[core.TokensKey]; ok {
		delete(md, core.TokensKey)
		tokens := []string{}
		if t != nil {
			for tok := range strings.SplitSeq(*t, ",") {
				if tok = strings.TrimSpace(tok); tok != "" {
					tokens = append(tokens, tok)
				}
			}
		}
		p.Tokens = &tokens
	}
	if len(md) > 0 {
		p.Metadata = md
	}
	return nil
}

// metadataValue reads one custom metadata value: a string, null (nil), or a boolean or number,
// stored as the text JSON.stringify gives it ("true", "-1"): @google-cloud/storage types the
// values string | boolean | number | null, and GCS stores them as strings (CT,
// gcs-js-sdk.test.ts:942-951; ADR-023 §3-77). An object or an array is not a value.
func metadataValue(v json.RawMessage) (*string, bool) {
	v = bytes.TrimSpace(v)
	switch {
	case len(v) == 0:
		return nil, false
	case isNull(v):
		return nil, true
	case v[0] == '"':
		var s string
		if json.Unmarshal(v, &s) != nil {
			return nil, false
		}
		return &s, true
	case string(v) == "true" || string(v) == "false":
		return new(string(v)), true
	case v[0] == '-' || ('0' <= v[0] && v[0] <= '9'):
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil || math.IsInf(f, 0) {
			return nil, false
		}
		return new(jsNumber(f)), true
	}
	return nil, false
}

// jsNumber writes f as JavaScript's Number#toString does: the shortest digits that read back
// to f, in decimal from 1e-6 to below 1e21 and with an exponent outside it (1e+21, 1.5e-7),
// and -0 as 0.
func jsNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	if a := math.Abs(f); a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // 1e+21, 1.5e-07
	mant, exp, _ := strings.Cut(s, "e")
	sign, digits := exp[:1], strings.TrimLeft(exp[1:], "0")
	return mant + "e" + sign + digits
}

func isNull(v json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(v), []byte("null")) }

// emptyJSON reports a value that sets nothing: null, false, "", [] or {}.
func emptyJSON(v json.RawMessage) bool {
	switch string(bytes.TrimSpace(v)) {
	case "null", "false", `""`, "[]", "{}":
		return true
	}
	return false
}
