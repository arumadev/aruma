package core

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// pushChars are the 64 characters of a push id, in ASCII order, so ids sort as their times do.
const pushChars = "-0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"

// pushStepBits bounds the step between two ids of one millisecond: 1 to 2^pushStepBits. A
// millisecond then still holds at least 2^24 ids (64^12 = 2^72 random parts), far more than
// one writer makes, and guessing the next id from the last takes up to 2^48 tries.
const pushStepBits = 48

// PushIDs makes the keys a REST POST gives the children it adds, in the shape the SDKs' push()
// gives them (nextPushId in firebase-js-sdk core/util/NextPushId.ts; the emulator's POST
// answers ids of the same shape): 8 characters of the time in milliseconds, then 12 random
// ones. Two ids in the same millisecond take the previous random part plus a step, so ids from
// one generator sort in the order they were made. The SDK steps by one, which is enough for
// one client's own ids; this generator is shared by every REST client of the server, so its
// step is random (crypto/rand), and an id a client was given does not name the id the next
// client gets. A clock that steps back keeps the last time, so that ids still sort in order;
// the SDK does not guard against that.
type PushIDs struct {
	mu   sync.Mutex
	last int64
	rand [12]byte
}

// Next returns a new id for time now.
func (g *PushIDs) Next(now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := max(now.UnixMilli(), g.last)
	if ms != g.last {
		g.fill()
	} else if !g.step() {
		// The random part ran past the last id of this millisecond: move on to the next one.
		ms++
		g.fill()
	}
	g.last = ms
	var id [20]byte
	for i := 7; i >= 0; i-- {
		id[i] = pushChars[ms%64]
		ms /= 64
	}
	for i, r := range g.rand {
		id[8+i] = pushChars[r]
	}
	return string(id[:])
}

// step adds a random step, 1 to 2^pushStepBits, to the random part, read as a number in base
// 64. False when the sum does not fit.
func (g *PushIDs) step() bool {
	var b [8]byte
	_, _ = rand.Read(b[:])
	carry := binary.BigEndian.Uint64(b[:])>>(64-pushStepBits) + 1
	for i := len(g.rand) - 1; i >= 0 && carry > 0; i-- {
		sum := uint64(g.rand[i]) + carry
		g.rand[i], carry = byte(sum%64), sum/64
	}
	return carry == 0
}

func (g *PushIDs) fill() {
	_, _ = rand.Read(g.rand[:])
	for i := range g.rand {
		g.rand[i] &= 63
	}
}
