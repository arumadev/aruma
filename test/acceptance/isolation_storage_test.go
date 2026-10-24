package acceptance

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// Cloud Storage's isolation between projects (ADR-022 §3). This file is the contract's; each
// task that owns a case fills its own file: isolation_storage_s2c_test.go (download tokens, page
// tokens), _s2d_ (upload URLs), _s2e_ (rulesets), _s4a_ (service accounts).

// Each project has its own bucket, and a deleted project's bucket answers as one that never
// existed. The service is built on the server's control plane without the harness's Boot,
// which waits for S2-D.
func TestIsolationStorageBuckets(t *testing.T) {
	ie := newIsolation(t)
	ctx := context.Background()
	master, err := base64.StdEncoding.DecodeString(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypto.NewKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	disk, err := blob.NewDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := storage.New(ctx, ie.Pool, disk, storage.Options{
		Limits: storage.DefaultLimits(), Keys: keys, Projects: ie.App.Service,
		Inflight: rules.NewInflight(1 << 20), DataDir: dir,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	domain := ie.App.Config.ProjectDomain
	numbers := map[string]int64{}
	for _, x := range []struct {
		c  *Client
		id string
	}{{ie.a, "alpha-app"}, {ie.b, "bravo-app"}} {
		p := x.c.mustOK(x.c.do("GET", "/console/v1/projects/"+x.id, nil))
		b, err := st.Bucket(ctx, p.str("storageBucket"))
		if err != nil || b.Name != x.id+"."+domain {
			t.Fatalf("%s's bucket %q: %+v, %v", x.id, p.str("storageBucket"), b, err)
		}
		numbers[x.id] = b.Project
	}
	if numbers["alpha-app"] == numbers["bravo-app"] {
		t.Fatal("two projects resolve to one bucket")
	}
	for _, name := range []string{"alpha-app." + domain + ".", "Alpha-App." + domain, "alpha-app", "alpha-app.appspot.com", "nobody-app." + domain} {
		if _, err := st.Bucket(ctx, name); !errors.Is(err, storage.ErrBucketNotFound) {
			t.Errorf("Bucket(%q) = %v, want ErrBucketNotFound", name, err)
		}
	}
	ie.a.mustOK(ie.a.do("POST", "/console/v1/projects/alpha-app:delete", map[string]any{"confirmProjectId": "alpha-app"}))
	if _, err := st.Bucket(ctx, "alpha-app."+domain); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Errorf("a deleted project's bucket: %v, want ErrBucketNotFound", err)
	}
	if b, err := st.Bucket(ctx, "bravo-app."+domain); err != nil || b.Project != numbers["bravo-app"] {
		t.Errorf("B's bucket after A's deletion: %+v, %v", b, err)
	}
}
