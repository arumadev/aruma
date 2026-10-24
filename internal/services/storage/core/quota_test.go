package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The quota and the disk floor (ADR-022 §6.5).

// limited is an env whose project quota is quota bytes.
func limited(t *testing.T, quota int64) *env {
	return newEnv(t, func(o *Options) {
		o.Limits.ProjectBytes = quota
		o.Limits.ObjectBytes = quota
	})
}

// A start reserves nothing, whatever it declares; it only checks that the declaration fits.
func TestStartReservesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for range 5 {
		e.start(e.a, asUser("u1"), UploadSpec{Name: "big", Surface: SurfaceFirebase, Declared: 5 << 30})
		e.start(e.a, Principal{Kind: PrincipalAnonymous}, UploadSpec{Name: "big", Surface: SurfaceFirebase, Declared: 1 << 30})
	}
	if _, reserved, _, _ := e.usage(projectA); reserved != 0 {
		t.Errorf("ten starts of 1 and 5 GiB reserve %d bytes", reserved)
	}
	if _, err := e.s.StartUpload(ctx, e.a, Principal{Kind: PrincipalAnonymous}, UploadSpec{Name: "big", Surface: SurfaceFirebase, Declared: 2 << 30}); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("an anonymous start declaring more than its share: %v", err)
	}
	small := limited(t, 1<<20)
	if _, err := small.s.StartUpload(ctx, small.a, asUser("u1"), UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: 2 << 20}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a start over object_bytes: %v", err)
	}
	small.insert(small.a, "fill", strings.Repeat("x", 600<<10))
	if _, err := small.s.StartUpload(ctx, small.a, asUser("u1"), UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: 500 << 10}); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("a start that does not fit: %v", err)
	}
	// Anonymous: the declaration must fit the anonymous share too (20% of 1 MiB).
	if _, err := small.s.StartUpload(ctx, small.a, Principal{Kind: PrincipalAnonymous}, UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: 300 << 10}); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("an anonymous start over its share: %v", err)
	}
	small.start(small.a, Principal{Kind: PrincipalAnonymous}, UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: 100 << 10})
}

// A hundred uploads at once, each through the check alone, never pass the quota together.
func TestQuotaUnderConcurrency(t *testing.T) {
	const quota, size = 10 << 20, 300 << 10
	for _, mode := range []string{"insert", "session", "unknown length"} {
		t.Run(mode, func(t *testing.T) {
			e := limited(t, quota)
			ctx := context.Background()
			var wg sync.WaitGroup
			var stored, refused atomic.Int64
			for i := range 100 {
				wg.Go(func() {
					name := fmt.Sprint("f", i)
					data := bytes.Repeat([]byte{byte(i)}, size)
					var err error
					if mode == "insert" {
						_, err = e.s.Insert(ctx, e.a, asUser("u1"), uploadSpec(name, SurfaceFirebase), bytes.NewReader(data), size)
					} else {
						var id string
						if id, err = e.s.StartUpload(ctx, e.a, asUser(name), uploadSpec(name, SurfaceFirebase)); err == nil {
							length := int64(size)
							if mode == "unknown length" {
								length = -1 // Node's single request
							}
							_, err = e.s.WriteUpload(ctx, e.a, name, id, nil, Chunk{Body: bytes.NewReader(data), Length: length, Total: -1, Finalize: true})
						}
					}
					switch {
					case err == nil:
						stored.Add(1)
					case errors.Is(err, ErrQuotaExceeded):
						refused.Add(1)
					default:
						t.Error(err)
					}
				})
			}
			wg.Wait()
			used, reserved, _, _ := e.usage(projectA)
			// Bodies of unknown length reserve in steps (8 MiB, halved near the quota), so under
			// contention some are refused that would have fitted; never more than fits is stored.
			want := stored.Load() == quota/size
			if mode == "unknown length" {
				want = stored.Load() > 0
			}
			if used > quota || used != stored.Load()*size || reserved != 0 || !want {
				t.Errorf("%d stored and %d refused: %d used, %d reserved, quota %d", stored.Load(), refused.Load(), used, reserved, quota)
			}
			e.checkUsage(projectA)
		})
	}
}

// Sessions without an identity hold at most anonymous_staged_fraction of the quota; the rest
// stays for signed-in users. The refusal is 402 and final.
func TestAnonymousShare(t *testing.T) {
	e := limited(t, 10<<20)
	anon := Principal{Kind: PrincipalAnonymous}
	mib := strings.Repeat("x", 1<<20)
	ids := []string{e.start(e.a, anon, uploadSpec("a1", SurfaceFirebase)), e.start(e.a, anon, uploadSpec("a2", SurfaceFirebase)),
		e.start(e.a, anon, uploadSpec("a3", SurfaceFirebase))}
	for i, id := range ids[:2] {
		if _, err := e.chunk(e.a, fmt.Sprint("a", i+1), id, 0, mib, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.chunk(e.a, "a3", ids[2], 0, "x", false)
	if !errors.Is(err, ErrQuotaExceeded) || got.Status != UploadFinal {
		t.Fatalf("over the anonymous share: %+v, %v", got, err)
	}
	if v := metricValue(t, e.s.metrics.Rejections, "anonymous_quota"); v != 1 {
		t.Errorf("anonymous_quota rejections: %v", v)
	}
	// A user still has the rest.
	id := e.start(e.a, asUser("u1"), uploadSpec("u", SurfaceFirebase))
	if _, err := e.chunk(e.a, "u", id, 0, strings.Repeat("y", 7<<20), true); err != nil {
		t.Fatalf("a user next to the anonymous: %v", err)
	}
	// Finalizing an anonymous upload frees its share.
	if _, err := e.chunk(e.a, "a1", ids[0], 1<<20, "", true); err != nil {
		t.Fatal(err)
	}
	if _, _, anonymous, _ := e.usage(projectA); anonymous != 1<<20 {
		t.Errorf("%d anonymous bytes held after one finalize", anonymous)
	}
	e.checkUsage(projectA)
}

// Empty sessions count at start against the address's cap, so one client cannot make rows and
// files without bound, and are gone after empty_session_ttl, so they hold nothing for long.
func TestEmptySessions(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Limits.UploadSessionsPerIP = 3 })
	ctx := context.Background()
	ip := netip.MustParseAddr("2001:db8::1")
	sp := uploadSpec("a", SurfaceFirebase)
	sp.IP = ip
	for range 3 {
		e.start(e.a, Principal{Kind: PrincipalAnonymous}, sp)
	}
	if _, err := e.s.StartUpload(ctx, e.a, Principal{Kind: PrincipalAnonymous}, sp); !errors.Is(err, ErrTooManyUploads) {
		t.Errorf("a fourth empty session from one address: %v", err)
	}
	e.clock.add(10*time.Minute + time.Second)
	if n, err := e.s.expireSessions(ctx); err != nil || n != 3 {
		t.Fatalf("expired %d, %v", n, err)
	}
	if e.count(`SELECT count(*) FROM storage.upload_sessions`) != 0 || e.count(`SELECT count(*) FROM storage.blob_gc`) != 3 {
		t.Error("the empty sessions or their files stay")
	}
	// The per-IP cap counts sessions that hold a byte, per project, by /64.
	for i := range 3 {
		sp.IP = netip.MustParseAddr(fmt.Sprintf("2001:db8::%d", i+10))
		id := e.start(e.a, Principal{Kind: PrincipalAnonymous}, sp)
		if _, err := e.chunk(e.a, "a", id, 0, "x", false); err != nil {
			t.Fatal(err)
		}
	}
	sp.IP = netip.MustParseAddr("2001:db8::ffff")
	if _, err := e.s.StartUpload(ctx, e.a, Principal{Kind: PrincipalAnonymous}, sp); !errors.Is(err, ErrTooManyUploads) {
		t.Errorf("a fourth live session from the /64: %v", err)
	}
	if _, err := e.s.StartUpload(ctx, e.b, Principal{Kind: PrincipalAnonymous}, sp); err != nil {
		t.Errorf("the same address in another project: %v", err)
	}
	sp.IP = netip.MustParseAddr("2001:db8:0:1::1")
	if _, err := e.s.StartUpload(ctx, e.a, Principal{Kind: PrincipalAnonymous}, sp); err != nil {
		t.Errorf("another /64: %v", err)
	}
}

func TestSessionLimitsPerUserAndProject(t *testing.T) {
	e := newEnv(t, func(o *Options) {
		o.Limits.UploadSessionsPerUser = 2
		o.Limits.UploadSessionsPerProject = 3
	})
	ctx := context.Background()
	live := func(p Principal) {
		id := e.start(e.a, p, uploadSpec("a", SurfaceFirebase))
		if _, err := e.chunk(e.a, "a", id, 0, "x", false); err != nil {
			t.Fatal(err)
		}
	}
	live(asUser("u1"))
	live(asUser("u1"))
	if _, err := e.s.StartUpload(ctx, e.a, asUser("u1"), uploadSpec("a", SurfaceFirebase)); !errors.Is(err, ErrTooManyUploads) {
		t.Errorf("a third session for u1: %v", err)
	}
	live(asUser("u2"))
	if _, err := e.s.StartUpload(ctx, e.a, asUser("u3"), uploadSpec("a", SurfaceFirebase)); !errors.Is(err, ErrTooManyUploads) {
		t.Errorf("a fourth session in the project: %v", err)
	}
}

// An idle session gives back what it holds beyond its durable bytes, and an empty idle one ends;
// the stored bytes stay counted, and the session can still finish.
func TestIdleRelease(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.start(e.a, asUser("u1"), uploadSpec("a", SurfaceFirebase))
	if _, err := e.chunk(e.a, "a", id, 0, "abc", false); err != nil {
		t.Fatal(err)
	}
	// What a crash in the middle of a chunk leaves: more reserved than received.
	for _, sql := range []string{
		`UPDATE storage.upload_sessions SET reserved_bytes = reserved_bytes + 1000`,
		`UPDATE storage.usage SET reserved_bytes = reserved_bytes + 1000`,
	} {
		if _, err := e.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := e.s.releaseIdle(ctx); err != nil || n != 0 {
		t.Fatalf("a session active an instant ago: %d, %v", n, err)
	}
	e.clock.add(time.Hour + time.Second)
	if n, err := e.s.releaseIdle(ctx); err != nil || n != 1 {
		t.Fatalf("released %d, %v", n, err)
	}
	if _, reserved, _, _ := e.usage(projectA); reserved != 3 {
		t.Errorf("%d reserved after the release", reserved)
	}
	e.checkUsage(projectA)
	if got, err := e.chunk(e.a, "a", id, 3, "def", true); err != nil || e.read(e.a, "a") != "abcdef" {
		t.Fatalf("finishing after the release: %+v, %v", got, err)
	}
	e.checkUsage(projectA)
}

// The disk floor is reserved, not just checked: concurrent requests take it in one atomic step,
// and below it a write is 503, which leaves a session active.
func TestDiskFloor(t *testing.T) {
	e := newEnv(t)
	e.store.free = e.s.limits.MinFreeBytes + 10<<20
	var wg sync.WaitGroup
	var ok atomic.Int64
	for range 50 {
		wg.Go(func() {
			if e.s.reserveDisk(1<<20) == nil {
				ok.Add(1)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 10 || e.s.diskInflight.Load() != 10<<20 {
		t.Fatalf("%d reservations of 1 MiB passed a floor 10 MiB away; %d in flight", ok.Load(), e.s.diskInflight.Load())
	}
	e.s.releaseDisk(10 << 20)
	id := e.start(e.a, asUser("u1"), uploadSpec("a", SurfaceFirebase))
	big := strings.Repeat("x", 11<<20)
	got, err := e.chunk(e.a, "a", id, 0, big, false)
	if !errors.Is(err, ErrUnavailable) || got.Status != UploadActive {
		t.Fatalf("a chunk below the floor: %+v, %v", got, err)
	}
	if _, err := e.s.Insert(context.Background(), e.a, asAdmin(), uploadSpec("b", SurfaceGCS), strings.NewReader(big), -1); !errors.Is(err, ErrUnavailable) {
		t.Errorf("an insert below the floor: %v", err)
	}
	if e.s.diskInflight.Load() != 0 {
		t.Errorf("%d bytes left in flight", e.s.diskInflight.Load())
	}
	if _, err := e.chunk(e.a, "a", id, 0, "small", true); err != nil {
		t.Errorf("a small chunk above the floor: %v", err)
	}
	e.checkUsage(projectA)
}

func TestUsage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if u, err := e.s.Usage(ctx, e.b); err != nil || u != (Usage{QuotaBytes: 5 << 30}) {
		t.Errorf("a project without an account: %+v, %v", u, err)
	}
	e.insert(e.a, "a", "abc")
	id := e.start(e.a, asUser("u1"), uploadSpec("b", SurfaceFirebase))
	if _, err := e.chunk(e.a, "b", id, 0, "de", false); err != nil {
		t.Fatal(err)
	}
	if u, err := e.s.Usage(ctx, e.a); err != nil || u != (Usage{UsedBytes: 3, ReservedBytes: 2, Objects: 1, QuotaBytes: 5 << 30}) {
		t.Errorf("Usage = %+v, %v", u, err)
	}
}
