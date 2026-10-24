package api

import (
	"cmp"
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// firebaseObject is an object as /v0 shows it (A §4, ADR-022 §9, §12): the numbers as decimal
// strings, the times with three fractional digits, contentEncoding identity when none is
// stored (as Storage reports it, PY-RR unset-content-encoding), and the download tokens in
// downloadTokens alone, comma-separated. core never puts firebaseStorageDownloadTokens in
// Metadata, so the custom metadata shows as stored; an empty map is left out (ADR-023 §3-40).
func firebaseObject(o core.Object) wire.FirebaseObject {
	return wire.FirebaseObject{
		Name:               o.Name,
		Bucket:             o.Bucket,
		Generation:         wire.Int(o.Generation),
		Metageneration:     wire.Int(o.Metageneration),
		ContentType:        o.ContentType,
		TimeCreated:        wire.Time(o.Created),
		Updated:            wire.Time(o.Updated),
		StorageClass:       "STANDARD",
		Size:               wire.Int(o.Size),
		MD5Hash:            wire.MD5(o.MD5),
		CRC32C:             wire.CRC32C(o.CRC32C),
		ETag:               wire.ETag(o.Generation, o.Metageneration),
		DownloadTokens:     core.FirebaseTokens(o),
		ContentEncoding:    cmp.Or(o.ContentEncoding, "identity"),
		ContentDisposition: o.ContentDisposition,
		Metadata:           o.Metadata,
		ContentLanguage:    o.ContentLanguage,
		CacheControl:       o.CacheControl,
	}
}

// writeObject answers with an object's resource: a metadata read, a change, a finished upload.
func writeObject(w http.ResponseWriter, status int, o core.Object) {
	writeJSON(w, status, firebaseObject(o))
}
