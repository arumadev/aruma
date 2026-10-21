package core

import (
	"crypto/rand"
	"sync"
	"time"
)

// pushChars are the 64 characters of a push id, in ASCII order, so ids sort as their times do.
const pushChars = "-0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"

// PushIDs makes the keys a REST POST gives the children it adds, exactly as the SDKs' push()
// makes them (nextPushId in firebase-js-sdk core/util/NextPushId.ts; the emulator's POST
// answers ids of the same shape): 8 characters of the time in milliseconds, then 12 random
// ones. Two ids in the same millisecond take the previous random part plus one, so ids from
// one generator sort in the order they were made. A clock that steps back keeps the last
// time, for the same reason; the SDK does not guard against that.
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
	if ms == g.last {
		i := len(g.rand) - 1
		for ; i >= 0 && g.rand[i] == 63; i-- {
			g.rand[i] = 0
		}
		if i >= 0 {
			g.rand[i]++
		} else {
			// All 64^12 ids of this millisecond are taken: move on to the next one.
			ms++
			g.fill()
		}
	} else {
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

func (g *PushIDs) fill() {
	_, _ = rand.Read(g.rand[:])
	for i := range g.rand {
		g.rand[i] &= 63
	}
}
