package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

func TestCORS(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// None by default, as a new Firebase bucket, and metageneration 1.
	if rules, err := f.s.CORS(ctx, f.b); err != nil || rules != nil {
		t.Fatalf("by default: %v, %v", rules, err)
	}
	info, err := f.s.BucketInfo(ctx, f.b)
	if err != nil || info.Metageneration != 1 || !info.Created.Equal(created) || !info.Updated.Equal(created) || info.CORS != nil || info.Bucket != f.b {
		t.Fatalf("BucketInfo: %+v, %v", info, err)
	}

	set := []CORSRule{
		{Origins: []string{"https://app.example.com"}, Methods: []string{"GET", "HEAD"}, ResponseHeaders: []string{"Content-Type"}, MaxAgeSeconds: 3600},
		{Origins: []string{"*"}, Methods: []string{"GET"}},
	}
	if _, err := f.s.SetCORS(ctx, f.b, alice, set); err != ErrPermissionDenied {
		t.Errorf("by a user: %v", err)
	}
	info, err = f.s.SetCORS(ctx, f.b, admin, set)
	if err != nil || info.Metageneration != 2 || !reflect.DeepEqual(info.CORS, set) || !info.Created.Equal(created) || !info.Updated.After(created) {
		t.Fatalf("SetCORS: %+v, %v", info, err)
	}
	if rules, err := f.s.CORS(ctx, f.b); err != nil || !reflect.DeepEqual(rules, set) {
		t.Errorf("CORS after: %+v, %v", rules, err)
	}
	var stored string
	if err := f.pool.QueryRow(ctx, "SELECT cors::text FROM storage.buckets").Scan(&stored); err != nil ||
		!strings.Contains(stored, `"origin": ["https://app.example.com"]`) || !strings.Contains(stored, `"responseHeader"`) {
		t.Errorf("stored as %s, %v; want GCS's field names", stored, err)
	}
	// An empty list removes every rule, and each change advances the metageneration.
	info, err = f.s.SetCORS(ctx, f.b, admin, nil)
	if err != nil || info.Metageneration != 3 || info.CORS != nil {
		t.Errorf("cleared: %+v, %v", info, err)
	}
	if got, err := f.s.BucketInfo(ctx, f.b); err != nil || got.Metageneration != 3 {
		t.Errorf("BucketInfo after: %+v, %v", got, err)
	}

	for _, bad := range [][]CORSRule{
		{{Origins: []string{""}}},
		{{Origins: []string{"https://a.example\r\nX-Evil: 1"}}},
		{{Origins: []string{"https://a.example, https://b.example"}}},
		{{Methods: []string{"GE T"}}},
		{{Methods: []string{""}}},
		{{ResponseHeaders: []string{"X:Y"}}},
		{{MaxAgeSeconds: -1}},
		{{Origins: []string{strings.Repeat("a", 64<<10)}}},
	} {
		var ie *InvalidError
		if _, err := f.s.SetCORS(ctx, f.b, admin, bad); !errors.As(err, &ie) {
			t.Errorf("%+v: %v, want 400", bad, err)
		}
	}

	// A bucket whose project went is not found; an outage is not.
	f.s.projects = fakeProjects{err: apierr.New(apierr.NotFound, "Requested entity was not found.")}
	if _, err := f.s.BucketInfo(ctx, f.b); err != ErrBucketNotFound {
		t.Errorf("a deleted project: %v", err)
	}
	f.s.projects = fakeProjects{project: tcore.Project{Number: testProject + 1, State: "ACTIVE", StorageBucket: f.b.Name}}
	if _, err := f.s.SetCORS(ctx, f.b, admin, set); err != ErrBucketNotFound {
		t.Errorf("a name now another project's: %v", err)
	}
	f.s.projects = fakeProjects{err: errors.New("connection refused")}
	if _, err := f.s.BucketInfo(ctx, f.b); !errors.Is(err, ErrUnavailable) {
		t.Errorf("an outage: %v", err)
	}
}

func TestMatchCORS(t *testing.T) {
	s := &Service{}
	app := CORSRule{Origins: []string{"https://app.example.com", "http://localhost:5173"}, Methods: []string{"GET", "HEAD"}, MaxAgeSeconds: 60}
	star := CORSRule{Origins: []string{"*"}, Methods: []string{"GET"}}
	anyMethod := CORSRule{Origins: []string{"https://app.example.com"}, Methods: []string{"*"}}
	for _, c := range []struct {
		rules          []CORSRule
		origin, method string
		allow          bool
		rule           CORSRule
	}{
		{nil, "https://app.example.com", "GET", false, CORSRule{}}, // the default: nothing
		{[]CORSRule{app}, "https://app.example.com", "GET", true, app},
		{[]CORSRule{app}, "http://localhost:5173", "HEAD", true, app},
		{[]CORSRule{app}, "https://app.example.com", "PUT", false, CORSRule{}},
		{[]CORSRule{app}, "https://evil.example.com", "GET", false, CORSRule{}},
		{[]CORSRule{app}, "https://app.example.com.evil.com", "GET", false, CORSRule{}},
		{[]CORSRule{app}, "https://APP.example.com", "GET", false, CORSRule{}},
		{[]CORSRule{app}, "https://app.example.com", "get", false, CORSRule{}},
		{[]CORSRule{app}, "", "GET", false, CORSRule{}},
		{[]CORSRule{app, star}, "https://evil.example.com", "GET", true, star},
		{[]CORSRule{star, app}, "https://app.example.com", "GET", true, star}, // the first that matches
		{[]CORSRule{star, app}, "https://app.example.com", "HEAD", true, app},
		{[]CORSRule{{Origins: []string{"https://*.example.com"}, Methods: []string{"GET"}}}, "https://app.example.com", "GET", false, CORSRule{}},
		{[]CORSRule{anyMethod}, "https://app.example.com", "DELETE", true, anyMethod}, // GCS: "*" is any method
		{[]CORSRule{anyMethod}, "https://app.example.com", "", false, CORSRule{}},
	} {
		allow, rule := s.MatchCORS(c.rules, c.origin, c.method)
		if allow != c.allow || !reflect.DeepEqual(rule, c.rule) {
			t.Errorf("MatchCORS(%v, %q, %q) = %v, %+v", c.rules, c.origin, c.method, allow, rule)
		}
	}
}
