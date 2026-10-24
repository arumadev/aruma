package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// A nil list on a page is written [], never null (ADR-022 §13).
func TestFirebaseListNeverNull(t *testing.T) {
	for _, l := range []FirebaseList{{}, {Prefixes: []string{}}, {Items: []FirebaseListItem{}}} {
		for _, enc := range []func(any) ([]byte, error){Marshal, json.Marshal} {
			b, err := enc(l)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != `{"prefixes":[],"items":[]}` {
				t.Errorf("%+v = %s", l, b)
			}
		}
	}
	b, _ := Marshal(FirebaseList{NextPageToken: "t", Prefixes: []string{"a/"},
		Items: []FirebaseListItem{{Name: "a/b", Bucket: "x"}}})
	if string(b) != `{"nextPageToken":"t","prefixes":["a/"],"items":[{"name":"a/b","bucket":"x"}]}` {
		t.Errorf("a page = %s", b)
	}
}

// A /v0 object with every field empty writes {}: nothing is null, nothing is "".
func TestFirebaseObjectEmpty(t *testing.T) {
	b, _ := Marshal(FirebaseObject{Metadata: map[string]string{}})
	if string(b) != `{}` {
		t.Errorf("%s", b)
	}
	b, _ = Marshal(FirebaseObject{Name: "a", Metadata: map[string]string{"k": ""}})
	if string(b) != `{"name":"a","metadata":{"k":""}}` {
		t.Errorf("%s", b)
	}
}

// Marshal writes what Google writes: no HTML escaping, no trailing newline.
func TestMarshal(t *testing.T) {
	b, err := Marshal(FirebaseObject{Name: "<a&b>.txt"})
	if err != nil || string(b) != `{"name":"<a&b>.txt"}` {
		t.Errorf("%s %v", b, err)
	}
	if _, err := Marshal(func() {}); err == nil {
		t.Error("no error on a value JSON cannot hold")
	}
}

// The GCS types write their keys in production's order where production shows it: the bucket
// and the list page of the public bucket in bucket_probe.json, recorded truncated.
func TestGoldenGCSKeyOrder(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(oracleDir, "production", "bucket_probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Probes []struct {
			Control  string
			Response struct{ Body string }
		}
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile(`"(\w+)":`)
	keys := func(s string) []string {
		var out []string
		for _, m := range key.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return out
	}
	full := func(v any) []string {
		b, _ := Marshal(v)
		return keys(string(b))
	}
	var bucket, list []string
	for _, p := range probe.Probes {
		if p.Control == "" {
			continue
		}
		if strings.Contains(p.Response.Body, `"storage#bucket"`) {
			bucket = keys(p.Response.Body)
		} else {
			list = keys(p.Response.Body)
		}
	}
	gotBucket := full(GCSBucket{Kind: KindBucket, SelfLink: "s", ID: "i", Name: "n", ProjectNumber: "1", Generation: "1",
		Metageneration: "17", Location: "US", StorageClass: "STANDARD", ETag: "CBE=", TimeCreated: "t", Updated: "u"})
	if len(bucket) < 11 || !reflect.DeepEqual(gotBucket[:len(bucket)-1], bucket[:len(bucket)-1]) {
		t.Errorf("bucket keys %v, production %v", gotBucket, bucket)
	}
	gotList := full(GCSObjects{Kind: KindObjects, NextPageToken: "t",
		Items: []GCSObject{{Kind: KindObject, ID: "i", Name: "n", Bucket: "b"}}})
	if len(list) < 5 || !reflect.DeepEqual(gotList[:len(list)], list) {
		t.Errorf("list keys %v, production %v", gotList, list)
	}
}

func TestGCSHelpers(t *testing.T) {
	const base = "https://aruma.test"
	if got := GCSSelfLink(base, "b", "a/b c.txt"); got != base+"/storage/v1/b/b/o/a%2Fb%20c.txt" {
		t.Error(got)
	}
	if got := GCSMediaLink(base, "b", "a/b", 7); got != base+"/download/storage/v1/b/b/o/a%2Fb?generation=7&alt=media" {
		t.Error(got)
	}
	if got := GCSBucketSelfLink(base, "b"); got != base+"/storage/v1/b/b" {
		t.Error(got)
	}
	if got := GCSObjectID("b", "a/b", 7); got != "b/a/b/7" {
		t.Error(got)
	}
	b, _ := Marshal(GCSObjects{Kind: KindObjects})
	if string(b) != `{"kind":"storage#objects"}` { // cleanup.json
		t.Errorf("%s", b)
	}
	b, _ = Marshal(NewGCSError(412, ReasonConditionNotMet, "Precondition Failed"))
	if string(b) != `{"error":{"code":412,"message":"Precondition Failed","errors":[{"domain":"global","reason":"conditionNotMet","message":"Precondition Failed"}]}}` {
		t.Errorf("%s", b)
	}
	b, _ = Marshal(GCSRewrite{Kind: KindRewrite, TotalBytesRewritten: "11", ObjectSize: "11", Done: true})
	if string(b) != `{"kind":"storage#rewriteResponse","totalBytesRewritten":"11","objectSize":"11","done":true}` {
		t.Errorf("%s", b)
	}
}

// The session URLs are absolute from the public URL, and their name reads back from the query.
func TestUploadURLs(t *testing.T) {
	const base = "https://aruma.test"
	u := FirebaseUploadURL(base, "chat-app.aruma.test", "dir with space/ملف #1?.txt", "AbC-12_x")
	if want := base + "/v0/b/chat-app.aruma.test/o?name=dir%20with%20space%2F%D9%85%D9%84%D9%81%20%231%3F.txt" +
		"&upload_id=AbC-12_x&upload_protocol=resumable"; u != want {
		t.Errorf("FirebaseUploadURL = %s", u)
	}
	if got := queryValue(t, u, "name"); got != "dir with space/ملف #1?.txt" {
		t.Errorf("name reads back as %q", got)
	}
	g := GCSUploadURL(base, "b", "a+b", "id")
	if g != base+"/upload/storage/v1/b/b/o?uploadType=resumable&name=a%2Bb&upload_id=id" || queryValue(t, g, "name") != "a+b" {
		t.Errorf("GCSUploadURL = %s", g)
	}
}
