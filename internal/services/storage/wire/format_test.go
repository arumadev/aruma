package wire

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

func TestTime(t *testing.T) {
	for _, c := range []struct {
		in   time.Time
		want string
	}{
		{time.Date(2026, 10, 23, 10, 0, 0, 123_456_789, time.UTC), "2026-10-23T10:00:00.123Z"},
		// Truncated, never rounded: .999999 stays in its second.
		{time.Date(2026, 12, 31, 23, 59, 59, 999_999_000, time.UTC), "2026-12-31T23:59:59.999Z"},
		{time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "2026-01-02T03:04:05.000Z"},
		// In UTC, whatever the zone.
		{time.Date(2026, 10, 24, 1, 30, 0, 7_000_000, time.FixedZone("AST", 3*3600)), "2026-10-23T22:30:00.007Z"},
	} {
		if got := Time(c.in); got != c.want {
			t.Errorf("Time(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every time production and the emulator wrote is three digits and a Z, and Time writes it back
// unchanged: production's (Pyric serverFields, 2026-07-21, and the public bucket of
// bucket_probe.json) and every timeCreated, updated and timeStorageClassUpdated recorded.
func TestGoldenTimes(t *testing.T) {
	times := []string{
		"2026-07-21T15:27:09.639Z", // Pyric stdlib-realstorage-p2-native-object-fields, serverFields
		"2016-08-25T18:11:15.759Z", // bucket_probe.json, gcp-public-data-landsat timeCreated
	}
	re := regexp.MustCompile(`"(?:timeCreated|updated|timeStorageClassUpdated|timeFinalized)":"([^"]*)"`)
	for _, e := range exchanges(t) {
		if b := e.jsonReply(); b != nil {
			for _, m := range re.FindAllSubmatch(b, -1) {
				times = append(times, string(m[1]))
			}
		}
	}
	if len(times) < 200 {
		t.Fatalf("only %d times", len(times))
	}
	for _, s := range times {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if got := Time(v); got != s {
			t.Errorf("Time(%q) = %q", s, got)
		}
	}
}

func TestInt(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 1: "1", 1784647629568292: "1784647629568292",
		math.MaxInt64: "9223372036854775807"} {
		if got := Int(n); got != want {
			t.Errorf("Int(%d) = %q", n, got)
		}
		if back, err := ParseLength(want); err != nil || back != n {
			t.Errorf("ParseLength(%q) = %d %v", want, back, err)
		}
	}
}

func TestChecksums(t *testing.T) {
	// The CRC32C of the empty string is 0, base64 of four zero bytes (B §7: "AAAAAA==").
	if got := CRC32C(0); got != "AAAAAA==" {
		t.Errorf("CRC32C(0) = %q", got)
	}
	if got := MD5(md5.Sum(nil)); got != "1B2M2Y8AsgTpgAmY7PhCfg==" { // A §4's example
		t.Errorf("MD5(empty) = %q", got)
	}
	for _, v := range []uint32{0, 1, 0xdeadbeef, math.MaxUint32} {
		if got, err := ParseCRC32C(CRC32C(v)); err != nil || got != v {
			t.Errorf("ParseCRC32C(CRC32C(%#x)) = %#x %v", v, got, err)
		}
	}
	sum := md5.Sum([]byte("aruma"))
	if got, err := ParseMD5(MD5(sum)); err != nil || got != sum {
		t.Errorf("ParseMD5 = %x %v", got, err)
	}
	for _, bad := range []string{"", "AAAAAA=", "AAAAAA", "AAAAAAA=", "AAAAAA==AAAA", "AAAAAB==", "!!!!!!==",
		"AAAAAAAA", "AAAA"} {
		if _, err := ParseCRC32C(bad); !errors.Is(err, ErrBadHash) {
			t.Errorf("ParseCRC32C(%q) = %v", bad, err)
		}
	}
	for _, bad := range []string{"", "1B2M2Y8AsgTpgAmY7PhCfg=", "1B2M2Y8AsgTpgAmY7PhCfg", "1B2M2Y8AsgTpgAmY7PhCfh==",
		"AAAAAA==", "1B2M2Y8AsgTpgAmY7PhCfg==AAAA"} {
		if _, err := ParseMD5(bad); !errors.Is(err, ErrBadHash) {
			t.Errorf("ParseMD5(%q) = %v", bad, err)
		}
	}
}

func TestParseHashHeader(t *testing.T) {
	crc, sum := uint32(0x538b2c06), md5.Sum([]byte("x"))
	both := Hashes{CRC32C: crc, HasCRC32C: true, MD5: sum, HasMD5: true}
	for _, c := range []struct {
		in   []string
		want Hashes
		err  bool
	}{
		{in: nil},
		{in: []string{"crc32c=" + CRC32C(crc)}, want: Hashes{CRC32C: crc, HasCRC32C: true}}, // Go's last chunk (B §5.7)
		{in: []string{HashHeader(crc, sum)}, want: both},
		{in: []string{"crc32c=" + CRC32C(crc) + ", md5=" + MD5(sum)}, want: both},
		{in: []string{"crc32c=" + CRC32C(crc), "md5=" + MD5(sum)}, want: both},
		{in: []string{"crc32c=U5UsBg=="}, want: Hashes{CRC32C: 0x53952c06, HasCRC32C: true}}, // admin_go.json
		{in: []string{""}, err: true},
		{in: []string{"crc32c="}, err: true},
		{in: []string{"sha256=" + CRC32C(crc)}, err: true},
		{in: []string{"CRC32C=" + CRC32C(crc)}, err: true},
		{in: []string{"crc32c=" + CRC32C(crc), "crc32c=" + CRC32C(crc)}, err: true},
		{in: []string{"crc32c=" + CRC32C(crc) + ","}, err: true},
		{in: []string{"md5=AAAA"}, err: true},
	} {
		got, err := ParseHashHeader(c.in)
		if c.err {
			if !errors.Is(err, ErrBadHash) {
				t.Errorf("%q: %+v %v", c.in, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: %+v %v, want %+v", c.in, got, err, c.want)
		}
	}
}

// The x-goog-hash of every whole download the emulator served is HashHeader of the bytes it
// sent, with no space after the comma (B §6.1).
func TestGoldenHashHeader(t *testing.T) {
	n := 0
	for _, e := range exchanges(t) {
		h := header(e.Response.Headers)
		v := h.Get(GoogHash)
		if v == "" || e.Response.Status != http.StatusOK || h.Get("Content-Encoding") != "" {
			continue
		}
		b, ok := e.Response.Body.bytes()
		if !ok {
			continue
		}
		if got := HashHeader(crc32.Checksum(b, castagnoli), md5.Sum(b)); got != v {
			t.Errorf("%v: %q, want %q", e, got, v)
		}
		n++
	}
	if n < 5 {
		t.Errorf("only %d downloads", n)
	}
}

// The resources the servers answered uploads with carry the checksums of the bytes uploaded:
// md5Hash is MD5 of them everywhere, crc32c is CRC32C of them on GCS. On /v0 the emulator writes
// crc32c as a decimal (D §2.2); Aruma writes base64 on both surfaces (ADR-022 §12), and the test
// checks the number is the same.
func TestGoldenUploadChecksums(t *testing.T) {
	var gcs, v0 int
	for _, e := range exchanges(t) {
		if e.Response.Status != http.StatusOK || header(e.Request.Headers).Get("Content-Type") == "" {
			continue
		}
		ct := header(e.Request.Headers).Get("Content-Type")
		raw, ok := e.Request.Body.bytes()
		reply := e.jsonReply()
		if !ok || reply == nil {
			continue
		}
		rr, err := NewRelatedReader(bytesReader(raw), ct, 1<<20)
		if err != nil {
			continue // not a multipart upload
		}
		media := readAll(t, rr.Media)
		var o struct{ MD5Hash, CRC32C string }
		if err := json.Unmarshal(reply, &o); err != nil {
			t.Fatal(err)
		}
		if got := MD5(md5.Sum(media)); got != o.MD5Hash {
			t.Errorf("%v: md5Hash %s, want %s", e, got, o.MD5Hash)
		}
		crc := crc32.Checksum(media, castagnoli)
		if isV0(e) {
			if got := strconv.FormatUint(uint64(crc), 10); got != o.CRC32C {
				t.Errorf("%v: crc32c %s, the emulator wrote %s", e, got, o.CRC32C)
			}
			v0++
		} else {
			if got := CRC32C(crc); got != o.CRC32C {
				t.Errorf("%v: crc32c %s, want %s", e, got, o.CRC32C)
			}
			gcs++
		}
	}
	if v0 < 20 || gcs < 5 {
		t.Errorf("only %d /v0 and %d GCS uploads", v0, gcs)
	}
}

func TestETag(t *testing.T) {
	const gen = 1784647629568292 // Pyric serverFields
	a, b := ETag(gen, 1), ETag(gen, 2)
	if a == b {
		t.Error("the etag field does not change with the metageneration")
	}
	if ETag(gen+1, 1) == a {
		t.Error("the etag field does not change with the generation")
	}
	// GCS's shape: base64 of field 1 = generation, field 2 = metageneration, as varints.
	raw, err := base64.StdEncoding.DecodeString(a)
	if err != nil || raw[0] != 0x08 {
		t.Fatalf("%q: %x %v", a, raw, err)
	}
	g, n := binary.Uvarint(raw[1:])
	if g != gen || raw[1+n] != 0x10 || raw[2+n] != 0x01 || len(raw) != 3+n {
		t.Errorf("%q decodes to %x", a, raw)
	}
	if got := ETag(1, 1); got != "CAEQAQ==" {
		t.Errorf("ETag(1, 1) = %q", got)
	}
}

// The bucket etag is production's: metageneration 17 is "CBE=" (bucket_probe.json).
func TestBucketETag(t *testing.T) {
	if got := BucketETag(17); got != "CBE=" {
		t.Errorf("BucketETag(17) = %q, want CBE=", got)
	}
}

// MediaETag follows the content alone: a PATCH or a minted token raises the metageneration and
// must not restart a download in progress (ADR-022 §12). It is a strong entity tag.
func TestMediaETag(t *testing.T) {
	const gen = 1784647629568292
	m := MediaETag(gen)
	if m != MediaETag(gen) {
		t.Error("MediaETag is not stable")
	}
	if MediaETag(gen+1) == m {
		t.Error("MediaETag does not change with the generation")
	}
	for metagen := int64(1); metagen < 5; metagen++ {
		if ETag(gen, metagen) == m || `"`+ETag(gen, metagen)+`"` == m {
			t.Errorf("MediaETag equals the resource etag at metageneration %d", metagen)
		}
	}
	// RFC 9110 §8.8.3: entity-tag = [ W/ ] DQUOTE *etagc DQUOTE, etagc = %x21 / %x23-7E.
	if !regexp.MustCompile(`^"[\x21\x23-\x7e]+"$`).MatchString(m) {
		t.Errorf("MediaETag %q is not a strong entity tag", m)
	}
	if got := MediaETag(1); got != `"CAE="` {
		t.Errorf("MediaETag(1) = %s", got)
	}
}

func TestHashHeader(t *testing.T) {
	sum := md5.Sum([]byte("hello"))
	crc := crc32.Checksum([]byte("hello"), castagnoli)
	want := "crc32c=" + CRC32C(crc) + ",md5=XUFAKrxLKna5cZ2REBfFkg=="
	if got := HashHeader(crc, sum); got != want {
		t.Errorf("HashHeader = %q, want %q", got, want)
	}
}
