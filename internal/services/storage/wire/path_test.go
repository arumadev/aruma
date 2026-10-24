package wire

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestSplitObjectPath(t *testing.T) {
	for _, c := range []struct {
		in   string
		want ObjectPath
		err  bool
	}{
		{in: "b/chat-app.aruma.test", want: ObjectPath{Bucket: "chat-app.aruma.test"}},
		{in: "b/x/o", want: ObjectPath{Bucket: "x", Objects: true}},
		{in: "b/x/o/", want: ObjectPath{Bucket: "x", Objects: true}}, // the web SDK's root reference
		{in: "b/x/o/a.txt", want: ObjectPath{Bucket: "x", Objects: true, Object: "a.txt"}},
		// %2F stays inside the name, decoded once (A §2.2).
		{in: "b/x/o/images%2Fa%20b.png", want: ObjectPath{Bucket: "x", Objects: true, Object: "images/a b.png"}},
		{in: "b/x/o/a%252Fb", want: ObjectPath{Bucket: "x", Objects: true, Object: "a%2Fb"}},
		// A path's "+" is a plus (B §2.3); iOS escapes it as %2B (A §2.2).
		{in: "b/x/o/1+1%2B2", want: ObjectPath{Bucket: "x", Objects: true, Object: "1+1+2"}},
		{in: "b/x/o/%D9%85%D9%84%D9%81%20%231%3F.txt", want: ObjectPath{Bucket: "x", Objects: true, Object: "ملف #1?.txt"}},
		// GCS's rewriteTo keeps the destination escaped.
		{in: "b/x/o/src%2Fa/rewriteTo/b/y/o/dst%2Fb", want: ObjectPath{Bucket: "x", Objects: true, Object: "src/a", Rest: "rewriteTo/b/y/o/dst%2Fb"}},
		// A raw "/" after the object starts Rest: no client sends one.
		{in: "b/x/o/a/b", want: ObjectPath{Bucket: "x", Objects: true, Object: "a", Rest: "b"}},
		{in: "b/x/acl", want: ObjectPath{Bucket: "x", Rest: "acl"}},
		{in: "b/x/iam/testPermissions", want: ObjectPath{Bucket: "x", Rest: "iam/testPermissions"}},
		{in: "", err: true},
		{in: "b", err: true},
		{in: "b/", err: true},
		{in: "b//o/a", err: true},
		{in: "x/b/o", err: true},
		{in: "/b/x/o", err: true},
		{in: "b/%zz/o", err: true},
		{in: "b/x/o/a%2", err: true},
		{in: "b/x/o/a%G0", err: true},
		{in: "b/x/o/a/rewriteTo/%", err: true},
		{in: "b/x/a%/b", err: true},
	} {
		got, err := SplitObjectPath(c.in)
		if c.err {
			if !errors.Is(err, ErrBadPath) {
				t.Errorf("%q: %+v, %v; want ErrBadPath", c.in, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
}

func TestCutFirebasePrefix(t *testing.T) {
	for in, want := range map[string]string{
		"/v0/b/x/o": "b/x/o",
		"/firebasestorage.googleapis.com/v0/b/x/o/a": "b/x/o/a",
	} {
		if got, ok := CutFirebasePrefix(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"/v1/b/x", "/storage/v1/b/x", "v0/b/x", "/firebasestorage.googleapis.com/b/x"} {
		if _, ok := CutFirebasePrefix(in); ok {
			t.Errorf("%q has a prefix", in)
		}
	}
}

// EscapeComponent is encodeURIComponent: what the web SDK writes (A §2.2), and what
// SplitObjectPath and a query read back.
func TestEscapeComponent(t *testing.T) {
	for in, want := range map[string]string{
		"a.txt":                 "a.txt",
		"images/a b.png":        "images%2Fa%20b.png",
		"1+1=2&x;y?#%":          "1%2B1%3D2%26x%3By%3F%23%25",
		"-_.!~*'()":             "-_.!~*'()",
		"ملف":                   "%D9%85%D9%84%D9%81",
		"\x00\x7f\xff":          "%00%7F%FF",
		"aruma-ref/s0c/g h.txt": "aruma-ref%2Fs0c%2Fg%20h.txt",
	} {
		got := EscapeComponent(in)
		if got != want {
			t.Errorf("EscapeComponent(%q) = %q, want %q", in, got, want)
		}
		p, err := SplitObjectPath("b/x/o/" + got)
		if err != nil || p.Object != in {
			t.Errorf("%q does not split back: %+v %v", got, p, err)
		}
		if q, err := url.ParseQuery("name=" + got); err != nil || q.Get("name") != in {
			t.Errorf("%q does not read back as a query: %v %v", got, q, err)
		}
	}
}

// Every path the clients sent, under every prefix they used, splits to the bucket of the run and
// to the name the server answered with.
func TestGoldenPaths(t *testing.T) {
	prefixes := []string{FirebasePrefix, GCSPrefix, GCSUploadPrefix, GCSDownloadPrefix, "/"}
	var paths, named int
	for _, e := range exchanges(t) {
		escaped, _, _ := strings.Cut(e.Request.URL, "?")
		var rest string
		for _, p := range prefixes {
			if r, ok := strings.CutPrefix(escaped, p); ok && strings.HasPrefix(r, "b/") {
				rest = r
				break
			}
		}
		if rest == "" {
			continue // Go's XML reads, /<bucket>/<object>, are not under b/
		}
		p, err := SplitObjectPath(rest)
		if err != nil || (p.Bucket != emulatorBucket && !strings.HasPrefix(p.Bucket, "no-such-bucket")) {
			t.Errorf("%v: %+v %v", e, p, err)
			continue
		}
		paths++
		if p.Object == "" || e.Response.Status != 200 {
			continue
		}
		b := e.jsonReply()
		if b == nil {
			continue
		}
		var reply struct {
			Name     string `json:"name"`
			Resource *struct {
				Name string `json:"name"`
			} `json:"resource"`
		}
		if json.Unmarshal(b, &reply) != nil {
			t.Fatal(e)
		}
		if p.Rest != "" { // rewriteTo/b/<bucket>/o/<object>
			r, ok := strings.CutPrefix(p.Rest, "rewriteTo/")
			dst, err := SplitObjectPath(r)
			if !ok || err != nil || reply.Resource == nil || dst.Object != reply.Resource.Name {
				t.Errorf("%v: rest %q: %+v %v", e, p.Rest, dst, err)
			}
			named++
			continue
		}
		if reply.Name != "" {
			if reply.Name != p.Object {
				t.Errorf("%v: object %q, the server answered %q", e, p.Object, reply.Name)
			}
			named++
		}
	}
	if paths < 200 || named < 30 {
		t.Errorf("only %d paths, %d named", paths, named)
	}
}
