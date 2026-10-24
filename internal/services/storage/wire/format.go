package wire

import "time"

// The value formats both surfaces share (ADR-022 §12). S2-B writes them.

// Time formats t as 2006-01-02T15:04:05.000Z, in UTC: three fractional digits and a Z, which
// Android and iOS require (A §1.6).
func Time(t time.Time) string { panic("S2-B: Time") }

// Int formats a number as the decimal string both surfaces send.
func Int(n int64) string { panic("S2-B: Int") }

// CRC32C formats a Castagnoli checksum as base64 of its four big-endian bytes, with padding.
func CRC32C(v uint32) string { panic("S2-B: CRC32C") }

// MD5 formats an MD5 sum as base64.
func MD5(sum [16]byte) string { panic("S2-B: MD5") }

// ETag returns the etag field of an object resource: opaque, and changing with every
// metageneration (ADR-022 §12).
func ETag(generation, metageneration int64) string { panic("S2-B: ETag") }

// MediaETag returns the ETag header of a media reply, from the generation alone, so a metadata
// change never restarts a download in progress (ADR-022 §12).
func MediaETag(generation int64) string { panic("S2-B: MediaETag") }

// HashHeader returns the x-goog-hash value, crc32c=<b64>,md5=<b64> with no space after the
// comma, which Node splits on (B §6.1).
func HashHeader(crc uint32, md5 [16]byte) string { panic("S2-B: HashHeader") }
