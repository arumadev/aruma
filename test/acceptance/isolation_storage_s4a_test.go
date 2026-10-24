package acceptance

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// S4-A's Storage isolation cases (ADR-022 §3); the file is S4-A's alone.

// A service account reads and changes its own project's bucket over GCS, and nothing of
// another's: every request on B's bucket with A's token is 403 forbidden, a rewrite from B's
// bucket into A's or from A's into B's included, and B's objects stay as they were. Without a
// token every request is 401. The tokens are real: each project's key file exchanged at
// Aruma's OAuth endpoint, as the Admin SDKs do.
func TestIsolationStorageServiceAccount(t *testing.T) {
	ie := newIsolationWith(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir()})
	st := ie.App.Storage
	ctx := context.Background()
	domain := ie.App.Config.ProjectDomain
	bucketA, bucketB := "alpha-app."+domain, "bravo-app."+domain
	ba, err := st.Bucket(ctx, bucketA)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := st.Bucket(ctx, bucketB)
	if err != nil {
		t.Fatal(err)
	}
	admin := storage.Principal{Kind: storage.PrincipalAdmin, ServiceAccount: "seed"}
	for _, x := range []struct {
		b    storage.Bucket
		body string
	}{{ba, "alpha's bytes"}, {bb, "bravo's bytes"}} {
		if _, err := st.Insert(ctx, x.b, admin, storage.UploadSpec{Name: "doc.txt", Declared: int64(len(x.body)), Surface: storage.SurfaceGCS},
			strings.NewReader(x.body), int64(len(x.body))); err != nil {
			t.Fatal(err)
		}
	}
	tokenA, _, err := exchange(t, ie.aKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, _, err := exchange(t, ie.bKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	sa := ie.withBearer(tokenA)
	anonymous := ie.client()

	// A's own bucket works.
	sa.mustOK(sa.do("GET", "/storage/v1/b/"+bucketA+"?alt=json&prettyPrint=false&projection=full", nil))
	sa.mustOK(sa.do("GET", "/storage/v1/b/"+bucketA+"/o/doc.txt", nil))
	sa.mustOK(sa.do("POST", "/storage/v1/b/"+bucketA+"/o/doc.txt/rewriteTo/b/"+bucketA+"/o/copy.txt", map[string]any{}))

	objB := "/storage/v1/b/" + bucketB + "/o/doc.txt"
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/storage/v1/b/" + bucketB, nil},
		{"PATCH", "/storage/v1/b/" + bucketB, map[string]any{"cors": []any{map[string]any{"origin": []string{"*"}, "method": []string{"GET"}}}}},
		{"GET", "/storage/v1/b/" + bucketB + "/o", nil},
		{"GET", objB, nil},
		{"GET", objB + "?alt=media", nil},
		{"GET", "/download" + objB + "?alt=media", nil},
		{"PATCH", objB, map[string]any{"metadata": map[string]string{"firebaseStorageDownloadTokens": "planted"}}},
		{"DELETE", objB, nil},
		{"POST", objB + "/rewriteTo/b/" + bucketA + "/o/stolen.txt", map[string]any{}},
		{"POST", "/storage/v1/b/" + bucketA + "/o/doc.txt/rewriteTo/b/" + bucketB + "/o/planted.txt", map[string]any{}},
		{"POST", "/upload/storage/v1/b/" + bucketB + "/o?uploadType=resumable&name=planted.txt", map[string]any{}},
		{"POST", "/upload/storage/v1/b/" + bucketB + "/o?uploadType=multipart&name=planted.txt", map[string]any{}},
	} {
		r := sa.do(c.method, c.path, c.body)
		var body struct {
			Error struct {
				Code   any `json:"code"`
				Errors []struct {
					Reason string `json:"reason"`
				} `json:"errors"`
			} `json:"error"`
		}
		_ = json.Unmarshal(r.Body, &body)
		if r.Status != 403 || body.Error.Code != float64(403) || len(body.Error.Errors) != 1 || body.Error.Errors[0].Reason != "forbidden" {
			t.Errorf("A's account: %s %s: %d %s", c.method, c.path, r.Status, r.Body)
		}
		if r := anonymous.do(c.method, c.path, c.body); r.Status != 401 {
			t.Errorf("no token: %s %s: %d %s", c.method, c.path, r.Status, r.Body)
		}
	}

	// B's objects are as they were, and only B's account reads them.
	sb := ie.withBearer(tokenB)
	var o struct {
		Name, Metageneration string
		Metadata             map[string]string
	}
	if err := json.Unmarshal(sb.mustOK(sb.do("GET", objB, nil)).Body, &o); err != nil || o.Metageneration != "1" || o.Metadata != nil {
		t.Errorf("B's object after A's requests: %+v, %v", o, err)
	}
	for _, name := range []string{"planted.txt", "stolen.txt"} {
		for _, b := range []string{bucketA, bucketB} {
			tok := map[string]string{bucketA: tokenA, bucketB: tokenB}[b]
			if r := ie.withBearer(tok).do("GET", "/storage/v1/b/"+b+"/o/"+url.PathEscape(name), nil); r.Status != 404 {
				t.Errorf("%s in %s: %d %s", name, b, r.Status, r.Body)
			}
		}
	}
	if u, err := st.Usage(ctx, bb); err != nil || u.Objects != 1 {
		t.Errorf("B's usage: %+v, %v", u, err)
	}
	if info, err := st.BucketInfo(ctx, bb); err != nil || info.Metageneration != 1 || len(info.CORS) != 0 {
		t.Errorf("B's bucket: %+v, %v", info, err)
	}
}
