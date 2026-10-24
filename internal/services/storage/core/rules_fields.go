package core

import (
	"encoding/base64"
	"encoding/binary"
	"maps"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// The rules' view of an object (ADR-022 §8): resource, and request.resource for a create and an
// update, key by key as Pyric observed a deployed bucket (PY-RR,
// stdlib-realstorage-request-resource-fields.json, digame-mas.appspot.com, 2026-09-27, JS SDK
// 12.13.0; and stdlib-realstorage-p2-native-object-fields.json, 2026-07-21). A table matched
// against those observations: a new observation changes this file alone.

// storageClass is the one class v0.3 stores.
const storageClass = "STANDARD"

// identityEncoding is contentEncoding as the rules see an object stored without one: PY-RR's
// create unset-identity-contentEncoding and unset-null-contentEncoding (DENY), and on update
// stored-update/*/encoding-identity, for objects uploaded by a client and by GCS alike.
const identityEncoding = "identity"

// incomingCreate is request.resource when an upload stores o (with its actual size and hashes,
// and the defaults it is stored with): PY-RR's fifteen keys (keys-size-15 ALLOW, 13, 14 and 16
// DENY; keys-documented-13 and extra-key-cacheControl, extra-key-storageClass ALLOW), with
// generation, metageneration, etag and storageClass null (generation-null, etag-null,
// storage-class-null ALLOW; generation-int, etag-string, storage-class-standard DENY), and no
// timeCreated or updated (present-timeCreated, time-created-key DENY). An upload over an
// existing object sees the same (overwrite/*).
func incomingCreate(o Object) rules.Value {
	m := ruleContentFields(o)
	m["generation"] = rules.Null()
	m["metageneration"] = rules.Null()
	m["etag"] = rules.Null()
	m["storageClass"] = rules.Null()
	return rules.Map(m)
}

// incomingUpdate is request.resource when a metadata change turns the object into o: the same
// fifteen keys with o's values, generation, metageneration and etag unchanged
// (generation-unchanged, metageneration-unchanged, etag-unchanged ALLOW; metageneration-next
// DENY), storageClass STANDARD (storage-class-standard ALLOW), and still no timeCreated or
// updated (time-created-unchanged, updated-key DENY).
func incomingUpdate(o Object) rules.Value {
	m := ruleContentFields(o)
	versionFields(m, o)
	return rules.Map(m)
}

// existing is resource, the object stored: the fifteen keys with their stored values, and
// timeCreated and updated as timestamps (the Storage reference; native-object-fields reads
// generation and metageneration as ints, md5Hash, crc32c and etag as GCS shows them, and
// timeCreated and updated with toMillis(); aruma/storage/resource_size and
// resource_time_created_is_ts). The exact set beyond those is not observed: provisional.
func existing(o Object) rules.Value {
	m := ruleContentFields(o)
	versionFields(m, o)
	m["timeCreated"] = rules.Timestamp(o.Created)
	m["updated"] = rules.Timestamp(o.Updated)
	return rules.Map(m)
}

// ruleContentFields are the eleven keys every view shares. An unset field is null
// (unset-null-contentLanguage, -cacheControl, -metadata ALLOW; unset-empty-* DENY) but
// contentEncoding, which is identity; contentDisposition is what the object stores, the
// default of ADR-022 §10.4 on /v0 (unset-filename-contentDisposition ALLOW) and null on GCS
// (stored-update/gcs/disposition-null ALLOW). md5Hash and crc32c are base64 as GCS shows them
// (md5-string, crc32c-string ALLOW; native-object-fields' stored-exact).
func ruleContentFields(o Object) map[string]rules.Value {
	encoding := o.ContentEncoding
	if encoding == "" {
		encoding = identityEncoding
	}
	return map[string]rules.Value{
		"name":               rules.String(o.Name),
		"bucket":             rules.String(o.Bucket),
		"size":               rules.Int(o.Size),
		"contentType":        ruleString(o.ContentType),
		"contentDisposition": ruleString(o.ContentDisposition),
		"contentEncoding":    rules.String(encoding),
		"contentLanguage":    ruleString(o.ContentLanguage),
		"cacheControl":       ruleString(o.CacheControl),
		"metadata":           metadataValue(o.Metadata),
		"md5Hash":            rules.String(base64.StdEncoding.EncodeToString(o.MD5[:])),
		"crc32c":             rules.String(crc32cBase64(o.CRC32C)),
	}
}

// versionFields adds the keys a stored object has and a new one does not have yet.
func versionFields(m map[string]rules.Value, o Object) {
	m["generation"] = rules.Int(o.Generation)
	m["metageneration"] = rules.Int(o.Metageneration)
	m["etag"] = rules.String(objectETag(o.Generation, o.Metageneration))
	m["storageClass"] = rules.String(storageClass)
}

// ruleString is s, null when unset.
func ruleString(s string) rules.Value {
	if s == "" {
		return rules.Null()
	}
	return rules.String(s)
}

// metadataValue is the custom metadata as a map, null when there is none (unset-null-metadata
// ALLOW, unset-empty-metadata DENY). The download tokens are never in it (ADR-022 §9).
//
// The map's order is provisional (docs/compat/storage-v0.3.md): rules.Map, by key. The Test API
// keeps the order a request sends, which values() follows (rules.MapInOrder; docs/compat/
// rules-v0.3.md), but it takes request verbatim, so it says nothing of the service; jsonb keeps
// no order, and a Patch's metadata is a Go map. Nothing measured shows what a deployed bucket
// gives values(), so no order-keeping storage is built on it.
func metadataValue(md map[string]string) rules.Value {
	n := len(md)
	if _, ok := md[TokensKey]; ok {
		n--
	}
	if n == 0 {
		return rules.Null()
	}
	m := make(map[string]rules.Value, n)
	for k, v := range md {
		if k != TokensKey {
			m[k] = rules.String(v)
		}
	}
	return rules.Map(m)
}

// crc32cBase64 is a CRC32C as GCS shows it: the four bytes big-endian, in base64 (ADR-022 §12).
func crc32cBase64(c uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], c)
	return base64.StdEncoding.EncodeToString(b[:])
}

// objectETag is an object's etag as GCS's JSON shows it: base64 of the protocol buffer
// {1: generation, 2: metageneration}, so it changes with every metageneration (ADR-022 §12;
// GCS's etags, e.g. CJDTyKyG4OkCEAE=, read so). wire.ETag (S2-B) must give the same string, or
// a rule comparing resource.etag with what a client read would not match.
func objectETag(generation, metageneration int64) string {
	b := make([]byte, 0, 2+2*binary.MaxVarintLen64)
	b = append(b, 0x08)
	b = binary.AppendUvarint(b, uint64(generation))
	b = append(b, 0x10)
	b = binary.AppendUvarint(b, uint64(metageneration))
	return base64.StdEncoding.EncodeToString(b)
}

// patched is the object a metadata change turns o into, as an update's request.resource shows
// it: content fields set or deleted, then the custom metadata cleared and merged key by key.
// The download tokens are not the rules' (Patch.Tokens, ADR-022 §9).
func patched(o Object, p *Patch) Object {
	if p == nil {
		return o
	}
	for name, v := range p.Fields {
		val := ""
		if v != nil {
			val = *v
		}
		switch name {
		case FieldContentType:
			o.ContentType = val
		case FieldContentEncoding:
			o.ContentEncoding = val
		case FieldContentDisposition:
			o.ContentDisposition = val
		case FieldContentLanguage:
			o.ContentLanguage = val
		case FieldCacheControl:
			o.CacheControl = val
		}
	}
	md := maps.Clone(o.Metadata)
	if p.ClearMetadata {
		md = nil
	}
	for k, v := range p.Metadata {
		if v == nil {
			delete(md, k)
			continue
		}
		if md == nil {
			md = map[string]string{}
		}
		md[k] = *v
	}
	delete(md, TokensKey)
	o.Metadata = md
	return o
}
