package gcs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/storage/wire"
)

func decodeBucket(t *testing.T, body []byte) (wire.GCSBucket, map[string]any) {
	t.Helper()
	var b wire.GCSBucket
	var raw map[string]any
	if json.Unmarshal(body, &b) != nil || json.Unmarshal(body, &raw) != nil {
		t.Fatalf("bucket %s", body)
	}
	return b, raw
}

// buckets.get as Node's exists() and Go's Attrs send it; an unknown bucket is production's 404,
// to anyone (bucket_probe.json).
func TestGetBucket(t *testing.T) {
	e := newEnv(t)
	for _, target := range []string{"/storage/v1/b/" + bucketA + "?alt=json&prettyPrint=false&projection=full", "/storage/v1/b/" + bucketA + "?"} {
		rec := e.do("GET", target, tokenA, nil)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
		}
		b, raw := decodeBucket(t, rec.Body.Bytes())
		if b.Kind != "storage#bucket" || b.ID != bucketA || b.Name != bucketA || b.ProjectNumber != "482915736204" ||
			b.Metageneration != "1" || b.ETag != "CAE=" || b.StorageClass != "STANDARD" || b.Location == "" ||
			b.SelfLink != baseURL+"/storage/v1/b/"+bucketA || b.TimeCreated == "" || b.CORS != nil {
			t.Errorf("bucket %s", rec.Body)
		}
		for _, k := range []string{"projectNumber", "metageneration"} {
			if _, ok := raw[k].(string); !ok {
				t.Errorf("%s is %T", k, raw[k])
			}
		}
	}
	for _, token := range []string{tokenA, "", tokenB} {
		body := gcsErr(t, e.do("GET", "/storage/v1/b/nobody-app.aruma.test", token, nil), 404, "notFound")
		if body.Message != wire.MessageNoSuchBucket {
			t.Errorf("unknown bucket: %q", body.Message)
		}
	}
	gcsErr(t, e.do("GET", "/storage/v1/b/"+bucketA, "", nil), 401, "required")
	gcsErr(t, e.do("GET", "/storage/v1/b/"+bucketA, "aruma_pat_cli", nil), 401, "authError")
	gcsErr(t, e.do("GET", "/storage/v1/b/"+bucketA, tokenB, nil), 403, "forbidden")
	gcsErr(t, e.do("GET", "/storage/v1/b/"+bucketA+"?ifMetagenerationMatch=2", tokenA, nil), 412, "conditionNotMet")
	gcsErr(t, e.do("GET", "/storage/v1/b/"+bucketA+"?ifGenerationMatch=1", tokenA, nil), 400, "invalid")
	if rec := e.do("GET", "/storage/v1/b/"+bucketA+"?ifMetagenerationNotMatch=2", tokenA, nil); rec.Code != 200 {
		t.Errorf("ifMetagenerationNotMatch: %d", rec.Code)
	}
}

// buckets.patch sets cors alone: the rules come back, the metageneration goes up, and any
// other field is 400.
func TestPatchBucket(t *testing.T) {
	e := newEnv(t)
	target := "/storage/v1/b/" + bucketA + "?alt=json&prettyPrint=false&projection=full"
	rec := e.do("PATCH", target, tokenA, strings.NewReader(
		`{"cors":[{"origin":["https://app.example"],"method":["GET","HEAD"],"responseHeader":["Content-Type"],"maxAgeSeconds":3600}]}`),
		"Content-Type", "application/json")
	if rec.Code != 200 {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	b, _ := decodeBucket(t, rec.Body.Bytes())
	if b.Metageneration != "2" || b.ETag != wire.BucketETag(2) || len(b.CORS) != 1 || b.CORS[0].MaxAgeSeconds != 3600 ||
		b.CORS[0].Origin[0] != "https://app.example" || len(b.CORS[0].Method) != 2 {
		t.Errorf("after the patch: %s", rec.Body)
	}
	got, _ := decodeBucket(t, e.do("GET", "/storage/v1/b/"+bucketA, tokenA, nil).Body.Bytes())
	if len(got.CORS) != 1 || got.Metageneration != "2" {
		t.Errorf("get after the patch: %+v", got)
	}
	// A body without cors changes nothing; null clears them.
	if b, _ := decodeBucket(t, e.do("PATCH", target, tokenA, strings.NewReader(`{}`)).Body.Bytes()); b.Metageneration != "2" {
		t.Errorf("empty patch: %+v", b)
	}
	b, _ = decodeBucket(t, e.do("PATCH", target+"&ifMetagenerationMatch=2", tokenA, strings.NewReader(`{"cors":null}`)).Body.Bytes())
	if b.CORS != nil || b.Metageneration != "3" {
		t.Errorf("cleared: %+v", b)
	}
	gcsErr(t, e.do("PATCH", target+"&ifMetagenerationMatch=2", tokenA, strings.NewReader(`{"cors":[]}`)), 412, "conditionNotMet")
	for _, body := range []string{`{"labels":{"a":"b"}}`, `{"cors":[],"versioning":{"enabled":true}}`, `{"cors":[{"origins":["x"]}]}`,
		`{"cors":{"origin":["x"]}}`, `{"cors":[{"maxAgeSeconds":"60"}]}`, `[]`} {
		gcsErr(t, e.do("PATCH", target, tokenA, strings.NewReader(body)), 400, "invalid")
	}
	gcsErr(t, e.do("PATCH", target+"&predefinedAcl=publicRead", tokenA, strings.NewReader(`{}`)), 501, "notImplemented")
	gcsErr(t, e.do("PATCH", target, tokenB, strings.NewReader(`{"cors":[]}`)), 403, "forbidden")
	gcsErr(t, e.do("PATCH", target, "", strings.NewReader(`{"cors":[]}`)), 401, "required")
	for _, m := range []string{"PUT", "DELETE"} {
		gcsErr(t, e.do(m, "/storage/v1/b/"+bucketA, tokenA, nil), 501, "notImplemented")
	}
}

// What v0.3 defers is 501 in GCS's shape; what the JSON API does not have is 404.
func TestDeferredAndUnknown(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, target string }{
		{"GET", "/storage/v1/b?project=alpha-app"},
		{"POST", "/storage/v1/b?project=alpha-app"},
		{"GET", "/storage/v1/projects/alpha-app/hmacKeys"},
		{"GET", "/storage/v1/b/" + bucketA + "/acl"},
		{"GET", "/storage/v1/b/" + bucketA + "/iam"},
		{"POST", "/storage/v1/b/" + bucketA + "/notificationConfigs"},
		{"GET", "/storage/v1/b/" + bucketA + "/o/a/acl"},
		{"POST", "/storage/v1/b/" + bucketA + "/o/a/moveTo/o/b"},
		{"POST", "/storage/v1/b/" + bucketA + "/o"},
	} {
		gcsErr(t, e.do(c.method, c.target, tokenA, nil), 501, "notImplemented")
	}
	gcsErr(t, e.do("GET", objA+"a", tokenA, nil, "X-Goog-Encryption-Algorithm", "AES256"), 501, "notImplemented")
	for _, c := range []struct{ method, target string }{
		{"GET", "/storage/v1/other"},
		{"GET", "/storage/v1/b/" + bucketA + "/whatever"},
		{"POST", objA + "a"},
		{"GET", objA + "a/whatever"},
		{"DELETE", "/storage/v1/b/" + bucketA + "/o"},
		{"PATCH", "/download" + objA + "a"},
	} {
		gcsErr(t, e.do(c.method, c.target, tokenA, nil), 404, "notFound")
	}
	gcsErr(t, e.do("GET", "/storage/v1/b//o", tokenA, nil), 400, "invalid")
}
