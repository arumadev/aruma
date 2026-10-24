package wire

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"time"
)

// The value formats both surfaces share (ADR-022 §12).

// timeLayout has three fractional digits and a Z: Android shifts a time with a longer fraction
// and reads 0 without one, and iOS reads nil (A §1.6).
const timeLayout = "2006-01-02T15:04:05.000Z"

// Time formats t as 2006-01-02T15:04:05.000Z, in UTC: three fractional digits and a Z, which
// Android and iOS require (A §1.6). The microseconds Postgres keeps are truncated, never
// rounded, so a time is never written later than it was.
func Time(t time.Time) string { return t.UTC().Format(timeLayout) }

// Int formats a number as the decimal string both surfaces send.
func Int(n int64) string { return strconv.FormatInt(n, 10) }

// CRC32C formats a Castagnoli checksum as base64 of its four big-endian bytes, with padding.
func CRC32C(v uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return base64.StdEncoding.EncodeToString(b[:])
}

// MD5 formats an MD5 sum as base64.
func MD5(sum [16]byte) string { return base64.StdEncoding.EncodeToString(sum[:]) }

// ErrBadHash is a checksum a client sent that is not base64 of the right number of bytes, or an
// x-goog-hash with an unknown, empty or repeated entry.
var ErrBadHash = errors.New("wire: malformed checksum")

// ParseCRC32C reads a crc32c a client sent in JSON or in X-Goog-Hash: standard base64, with
// padding, of four bytes, as Go and Node send it (B §5.8). Anything else is ErrBadHash.
func ParseCRC32C(s string) (uint32, error) {
	if len(s) != 8 {
		return 0, ErrBadHash
	}
	var b [6]byte // room for the decoder's last quantum
	if n, err := base64.StdEncoding.Strict().Decode(b[:], []byte(s)); err != nil || n != 4 {
		return 0, ErrBadHash
	}
	return binary.BigEndian.Uint32(b[:4]), nil
}

// ParseMD5 reads an md5Hash a client sent: standard base64, with padding, of sixteen bytes.
func ParseMD5(s string) ([16]byte, error) {
	var sum [16]byte
	if len(s) != 24 {
		return sum, ErrBadHash
	}
	var b [18]byte // room for the decoder's last quantum
	if n, err := base64.StdEncoding.Strict().Decode(b[:], []byte(s)); err != nil || n != 16 {
		return sum, ErrBadHash
	}
	copy(sum[:], b[:16])
	return sum, nil
}

// Hashes are the checksums of an X-Goog-Hash header, each one present or not.
type Hashes struct {
	CRC32C    uint32
	HasCRC32C bool
	MD5       [16]byte
	HasMD5    bool
}

// ParseHashHeader reads the values of a request's X-Goog-Hash headers: Go sends crc32c=<b64> on
// its last chunk (B §5.7), and each value may list several entries separated by commas. An
// entry that is not crc32c or md5, an empty one, or a checksum given twice is ErrBadHash: a
// header the server cannot check must not pass as checked.
func ParseHashHeader(values []string) (Hashes, error) {
	var hs Hashes
	for _, v := range values {
		for entry := range strings.SplitSeq(v, ",") {
			key, val, ok := strings.Cut(strings.Trim(entry, " \t"), "=")
			if !ok {
				return Hashes{}, ErrBadHash
			}
			switch key {
			case "crc32c":
				c, err := ParseCRC32C(val)
				if err != nil || hs.HasCRC32C {
					return Hashes{}, ErrBadHash
				}
				hs.CRC32C, hs.HasCRC32C = c, true
			case "md5":
				m, err := ParseMD5(val)
				if err != nil || hs.HasMD5 {
					return Hashes{}, ErrBadHash
				}
				hs.MD5, hs.HasMD5 = m, true
			default:
				return Hashes{}, ErrBadHash
			}
		}
	}
	return hs, nil
}

// ETag returns the etag field of an object resource: opaque, and changing with every
// metageneration (ADR-022 §12). It has the shape GCS gives it, base64 of the two numbers as
// protobuf varints (fields 1 and 2), such as CMDw…EAE= (B §7).
func ETag(generation, metageneration int64) string {
	b := make([]byte, 0, 2+2*binary.MaxVarintLen64)
	b = append(b, 0x08)
	b = binary.AppendUvarint(b, uint64(generation))
	b = append(b, 0x10)
	b = binary.AppendUvarint(b, uint64(metageneration))
	return base64.StdEncoding.EncodeToString(b)
}

// BucketETag returns the etag of a bucket resource, from its metageneration as GCS does: its
// metageneration 17 is "CBE=" (production, bucket_probe.json).
func BucketETag(metageneration int64) string {
	b := make([]byte, 0, 1+binary.MaxVarintLen64)
	b = append(b, 0x08)
	b = binary.AppendUvarint(b, uint64(metageneration))
	return base64.StdEncoding.EncodeToString(b)
}

// MediaETag returns the ETag header of a media reply, from the generation alone, so a metadata
// change never restarts a download in progress (ADR-022 §12). It is a strong entity tag (quoted,
// no W/), since If-Range compares it strongly, and differs from every ETag of a JSON resource.
func MediaETag(generation int64) string {
	b := make([]byte, 0, 1+binary.MaxVarintLen64)
	b = append(b, 0x08)
	b = binary.AppendUvarint(b, uint64(generation))
	return `"` + base64.StdEncoding.EncodeToString(b) + `"`
}

// HashHeader returns the x-goog-hash value, crc32c=<b64>,md5=<b64> with no space after the
// comma, which Node splits on (B §6.1).
func HashHeader(crc uint32, md5 [16]byte) string {
	return "crc32c=" + CRC32C(crc) + ",md5=" + MD5(md5)
}
