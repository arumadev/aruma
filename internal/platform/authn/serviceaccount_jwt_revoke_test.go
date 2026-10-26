package authn

import (
	"context"
	"sync"
	"testing"

	"github.com/arumadev/aruma/internal/platform/apierr"
)

// stalledDirectory hands out the record it read, but holds the lookup until released: a
// verification that read the account before a revocation and returns after it.
type stalledDirectory struct {
	*fakeDirectory
	read    chan struct{} // closed once a lookup has read its record
	release chan struct{}
	once    sync.Once
}

func (d *stalledDirectory) LookupServiceAccount(ctx context.Context, email string) (ServiceAccountRecord, error) {
	r, err := d.fakeDirectory.LookupServiceAccount(ctx, email)
	d.once.Do(func() { close(d.read) })
	<-d.release
	return r, err
}

// ADR-029 §3.8: once ForgetKey returns, a JWT the key signed is refused, whether it was cached
// before the call or was being verified against the key while the call ran. Run with -race.
func TestServiceAccountJWTForgetKeyRefusesAtOnce(t *testing.T) {
	v, dir, c, _ := newTestVerifier(t)
	k := testKeys()
	tok := goJWT(t, k[0], c.now())

	// Cached, then hammered by concurrent callers while the key goes.
	if _, err := v.Verify(t.Context(), tok); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), tok); err != nil || dir.lookups != 1 {
		t.Fatalf("not cached: %v, %d lookups", err, dir.lookups)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			for range 50 {
				_, _ = v.Verify(context.Background(), tok)
			}
		})
	}
	close(start)
	dir.deleteKey(testEmail, k[0].id)
	v.ForgetKey(k[0].id)
	if _, err := v.Verify(t.Context(), tok); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("right after ForgetKey: %v, want UNAUTHENTICATED", err)
	}
	wg.Wait()
	if _, err := v.Verify(t.Context(), tok); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("after the concurrent callers: %v, want UNAUTHENTICATED", err)
	}

	// A verification in flight across the revocation succeeds on what it read, but must not
	// cache it.
	stalled := &stalledDirectory{fakeDirectory: dir, read: make(chan struct{}), release: make(chan struct{})}
	v.dir = stalled
	tok = nodeJWT(t, k[1], c.now())
	done := make(chan error, 1)
	go func() {
		_, err := v.Verify(context.Background(), tok)
		done <- err
	}()
	<-stalled.read
	dir.deleteKey(testEmail, k[1].id)
	v.ForgetKey(k[1].id)
	close(stalled.release)
	if err := <-done; err != nil {
		t.Fatalf("the verification in flight: %v", err)
	}
	if _, err := v.Verify(t.Context(), tok); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("a verification that straddled ForgetKey cached its token: %v", err)
	}
}
