// Package core is Cloud Storage for Firebase apart from any transport (ADR-022): objects and
// their metadata, download tokens, lists, the upload sessions both upload protocols run on, the
// one transaction that makes a file visible, quota, the rules binding, and the life of the data
// directory (boot, sweep, GC, purge). The surfaces above it are api (Firebase REST, /v0), gcs
// (the GCS JSON API) and console; media serves the bytes. core knows no HTTP and no wire format.
//
// One bucket per project in v0.3: <project_id>.<project_domain>, resolved through tenancy, and
// keyed in every table by the project number (ADR-022 §2, §3).
//
// The provisional decisions of ADR-023 §3, which no production evidence settles yet, each live in
// one place, so evidence changes the place and not the design:
//
//  1. list matches the prefix's children (UnknownSegment): rules.go (S2-E).
//  2. request.method is the method's name, request.params an empty map: rules.go.
//  3. request.resource is null on get, list and delete: rules.go.
//  4. crc32c in base64 on /v0: wire.CRC32C, api/resource.go.
//  5. "metadata": null in a PATCH clears the custom metadata: metadata.go (S2-C).
//  6. A token opens alt=media only, not the metadata: objects.go (Open, S2-C).
//  7. A token that is present and does not match is ErrPermissionDenied, without the rules:
//     objects.go (Open).
//  8. A client's md5Hash is checked on /v0 too, 400 and final: uploads.go and insert.go (S2-D).
//  9. Cache-Control private, max-age=0 by default on a token link: media/headers.go (S2-F).
//  10. Quota: 402 on /v0, 403 quotaExceeded on GCS: api/errors.go, gcs/errors.go.
//  11. The 403 message "Permission denied." and the body of a repeated successful finalize:
//     api/errors.go, api/upload.go.
//  12. A wrong offset is 400 and active (OffsetError), a size other than declared 400 and final
//     (ErrSizeMismatch): uploads.go, api/upload.go.
//  13. Content-Disposition attachment on download without a stored value: media/headers.go; the
//     escaping of the stored default: defaultDisposition (helpers.go).
//  14. Authorization on chunks is ignored: api/upload.go.
//  15. CORS * on the API's replies and preflights: api/cors.go.
package core
