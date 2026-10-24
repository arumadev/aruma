package idtoken

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestStatus(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrExpired, "expired_token"},
		{ErrInvalid, "invalid_token"},
		{ErrRevoked, "invalid_token"},
		{ErrDisabled, "invalid_token"},
		{ErrUserNotFound, "invalid_token"},
		{fmt.Errorf("verify: %w", ErrExpired), "expired_token"},
		{errors.New("connection refused"), ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := Status(c.err); got != c.want {
			t.Errorf("Status(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestRevokes(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	c := &Claims{UID: "u1", AuthTime: at}
	cases := []struct {
		name string
		r    Revocation
		want bool
	}{
		{"disabled", Revocation{Project: 1, UID: "u1", Cause: Disabled}, true},
		{"deleted", Revocation{Project: 1, UID: "u1", Cause: Deleted}, true},
		{"session before the point", Revocation{Project: 1, UID: "u1", Cause: TokensRevoked, ValidAfter: at.Add(time.Second)}, true},
		{"session the change started", Revocation{Project: 1, UID: "u1", Cause: PasswordChanged, ValidAfter: at}, false},
		{"same second, at second precision", Revocation{Project: 1, UID: "u1", Cause: PasswordChanged, ValidAfter: at.Add(500 * time.Millisecond)}, false},
		{"other user", Revocation{Project: 1, UID: "u2", Cause: Disabled}, false},
		{"other project", Revocation{Project: 2, UID: "u1", Cause: Disabled}, false},
		{"other tenant", Revocation{Project: 1, Tenant: "t1", UID: "u1", Cause: Disabled}, false},
	}
	for _, tc := range cases {
		if got := tc.r.Revokes(1, c); got != tc.want {
			t.Errorf("%s: Revokes = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHubDeliversPerProject(t *testing.T) {
	h := NewHub(4)
	a, b := h.Subscribe(1), h.Subscribe(1)
	other := h.Subscribe(2)
	h.Publish(Revocation{Project: 1, UID: "u1", Cause: Disabled})
	for _, s := range []*Subscription{a, b} {
		select {
		case r := <-s.C():
			if r.UID != "u1" || r.Cause != Disabled {
				t.Fatalf("got %+v", r)
			}
		default:
			t.Fatal("revocation not delivered")
		}
	}
	select {
	case r := <-other.C():
		t.Fatalf("another project's subscriber got %+v", r)
	default:
	}
	a.Close()
	a.Close()
	if _, open := <-a.C(); open {
		t.Fatal("closed subscription still open")
	}
	if a.Err() != nil {
		t.Fatalf("Close set %v", a.Err())
	}
	if n := h.Subscribers(1); n != 1 {
		t.Fatalf("%d subscribers after Close, want 1", n)
	}
}

// A subscriber that stops reading never holds up Publish: it is ended with ErrOverflow.
func TestHubOverflowDoesNotBlock(t *testing.T) {
	h := NewHub(2)
	slow, fast := h.Subscribe(1), h.Subscribe(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			h.Publish(Revocation{Project: 1, UID: fmt.Sprint(i), Cause: TokensRevoked})
			<-fast.C()
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
	n := 0
	for range slow.C() {
		n++
	}
	if n != 2 || !errors.Is(slow.Err(), ErrOverflow) {
		t.Fatalf("slow subscriber: %d delivered, err %v; want 2 and ErrOverflow", n, slow.Err())
	}
	if fast.Err() != nil || h.Subscribers(1) != 1 {
		t.Fatalf("fast subscriber: err %v, %d subscribers", fast.Err(), h.Subscribers(1))
	}
	slow.Close() // after an overflow: no panic, Err kept
	if !errors.Is(slow.Err(), ErrOverflow) {
		t.Fatal("Close after overflow cleared the error")
	}
}

func TestUserStateAdmits(t *testing.T) {
	signIn := time.Unix(1_700_000_000, 0)
	issued := signIn.Add(time.Hour)
	cases := []struct {
		name string
		u    UserState
		want error
	}{
		{"live", UserState{Exists: true, Created: signIn.Add(-time.Hour)}, nil},
		{"deleted", UserState{Disabled: true}, ErrUserNotFound},
		{"disabled", UserState{Exists: true, Disabled: true}, ErrDisabled},
		{"revoked after the sign-in", UserState{Exists: true, TokensValidAfter: signIn.Add(time.Second)}, ErrRevoked},
		// The session a revocation itself returns starts at the revocation and survives it.
		{"revoked at the sign-in", UserState{Exists: true, TokensValidAfter: signIn}, nil},
		{"within the second", UserState{Exists: true, TokensValidAfter: signIn.Add(500 * time.Millisecond)}, nil},
		{"an earlier user of the uid", UserState{Exists: true, Created: issued.Add(time.Second)}, ErrRevoked},
	}
	for _, c := range cases {
		if got := c.u.Admits(signIn, issued); got != c.want {
			t.Errorf("%s: Admits = %v, want %v", c.name, got, c.want)
		}
	}
}
