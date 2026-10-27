package webchannel_test

import (
	"io"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/webchannel"
)

// postRaw posts a forward body as it is, and returns the status and the answer.
func (c *client) postRaw(body string) (int, string) {
	c.t.Helper()
	c.rid++
	res := post(c.t, c.query("RID", strconv.Itoa(c.rid)), body)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func heapMiB() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc >> 20
}

// A session whose stream ended (a bad token) holds nothing but its closing arrays, for a short
// while that no request extends; it never counted against the project its handshake named, and
// a forward POST on it reads no request.
func TestEndedSessionReleased(t *testing.T) {
	x := newHarness(t, options{
		grpc:   docgrpc.Limits{StreamsPerProject: 4},
		timing: webchannel.Timing{Expiry: 3 * time.Second},
	})
	base := heapMiB()
	bad := map[string]string{"Authorization": "Bearer not-a-token"}
	big := addDoc(1, "aruma-ref-wc/"+strings.Repeat("x", 4<<20))
	var cs []*client
	for range 4 {
		c, _ := handshake(t, x.url, bad, big)
		cs = append(cs, c)
	}
	time.Sleep(200 * time.Millisecond)
	for _, c := range cs {
		if st, body := c.postRaw("count=1&ofs=1&req0___data__=" + url.QueryEscape(big)); st != http.StatusOK {
			t.Errorf("forward on an ended session: %d %s", st, body)
		}
	}
	// Requests every 500 ms, past the end and the expiry, do not keep them.
	for range 9 {
		time.Sleep(500 * time.Millisecond)
		for _, c := range cs {
			c.postRaw("count=0")
		}
	}
	if n := x.wc.Sessions(); n != 0 {
		t.Errorf("%d ended sessions kept", n)
	}
	if heap := heapMiB(); heap > base+8 {
		t.Errorf("heap %d MiB, from %d", heap, base)
	}
	x.commit(t, "aruma-ref-wc/ok", 1)
	c, as := handshake(t, x.url, admin(t, x), addDoc(1, "aruma-ref-wc/ok"))
	if len(as) != 1 {
		_, status := decode(t, as[1].payload)
		t.Fatalf("handshake refused: %s", status)
	}
	c.back("0").want(t, "ADD[1]", "change aruma-ref-wc/ok", "CURRENT[1]", "global")
}

// The closing arrays are still read after the stream ended.
func TestEndedSessionStatus(t *testing.T) {
	x := newHarness(t, options{timing: webchannel.Timing{Expiry: time.Second}})
	c, _ := handshake(t, x.url, map[string]string{"Authorization": "Bearer not-a-token"}, addDoc(1, "aruma-ref-wc/i"))
	time.Sleep(300 * time.Millisecond)
	c.back("0").wantStatus(t, "UNAUTHENTICATED ")
}

// targets reads the targets the backchannel adds, in order, until it is quiet.
func (b *backReader) targets(t *testing.T, quiet time.Duration) []string {
	t.Helper()
	var got []string
	for {
		select {
		case a, ok := <-b.arrays:
			if !ok {
				return got
			}
			b.c.aid = a.id
			if p := string(a.payload); p == `["noop"]` || p == `["close"]` {
				continue
			}
			r, status := decode(t, a.payload)
			if status != "" {
				t.Fatalf("status %s", status)
			}
			if s := shape(r); strings.HasPrefix(s, "ADD[") {
				got = append(got, s)
			}
		case <-time.After(quiet):
			return got
		}
	}
}

// Forward POSTs in flight at once (ForwardChannelRequestPool over h2) may arrive out of order: a
// map is held until the one before it comes, and a map already taken is not taken again.
func TestForwardOutOfOrder(t *testing.T) {
	x := newHarness(t, options{})
	x.commit(t, "aruma-ref-wc/a", 1)
	c, _ := handshake(t, x.url, admin(t, x), addDoc(1, "aruma-ref-wc/a"))
	b := c.back("0")
	b.want(t, "ADD[1]", "change aruma-ref-wc/a", "CURRENT[1]", "global")
	for _, body := range []string{
		"count=1&ofs=2&req0___data__=" + url.QueryEscape(addDoc(3, "aruma-ref-wc/a")),
		"count=1&ofs=1&req0___data__=" + url.QueryEscape(addDoc(2, "aruma-ref-wc/a")),
		// Again, as a retry would.
		"count=2&ofs=1&req0___data__=" + url.QueryEscape(addDoc(2, "aruma-ref-wc/a")) + "&req1___data__=" + url.QueryEscape(addDoc(3, "aruma-ref-wc/a")),
	} {
		if st, answer := c.postRaw(body); st != http.StatusOK {
			t.Fatalf("forward: %d %s", st, answer)
		}
	}
	if got := b.targets(t, time.Second); !slices.Equal(got, []string{"ADD[2]", "ADD[3]"}) {
		t.Errorf("targets %v", got)
	}

	// Sparse keys: a retried map with a new one past the maps in flight elsewhere.
	sparse := "count=2&ofs=3&req0___data__=" + url.QueryEscape(addDoc(4, "aruma-ref-wc/a")) +
		"&req3___data__=" + url.QueryEscape(addDoc(7, "aruma-ref-wc/a"))
	if st, answer := c.postRaw(sparse); st != http.StatusOK {
		t.Fatalf("sparse keys: %d %s", st, answer)
	}
	if got := b.targets(t, time.Second); !slices.Equal(got, []string{"ADD[4]"}) {
		t.Errorf("targets %v", got)
	}
	gap := "count=2&ofs=4&req0___data__=" + url.QueryEscape(addDoc(5, "aruma-ref-wc/a")) +
		"&req1___data__=" + url.QueryEscape(addDoc(6, "aruma-ref-wc/a"))
	if st, answer := c.postRaw(gap); st != http.StatusOK {
		t.Fatalf("gap: %d %s", st, answer)
	}
	if got := b.targets(t, time.Second); !slices.Equal(got, []string{"ADD[5]", "ADD[6]", "ADD[7]"}) {
		t.Errorf("targets %v", got)
	}
}

// A map held for one before it counts against message_bytes: one that does not fit while the
// stream has nothing to read is refused at once, and the map before it is still taken.
func TestForwardHeldBound(t *testing.T) {
	x := newHarness(t, options{limits: webchannel.Limits{MessageBytes: 4096, WriteStall: 5 * time.Second}})
	c, _ := handshake(t, x.url, admin(t, x), addDoc(1, "aruma-ref-wc/a"))
	b := c.back("0")
	b.want(t, "ADD[1]")
	held := "count=1&ofs=3&req0___data__=" + url.QueryEscape(addDoc(3, docs("h", 45)...))
	if st, answer := c.postRaw(held); st != http.StatusOK {
		t.Fatalf("held: %d %s", st, answer)
	}
	start := time.Now()
	if st, _ := c.postRaw("count=1&ofs=4&req0___data__=" + url.QueryEscape(addDoc(4, docs("i", 45)...))); st != http.StatusServiceUnavailable {
		t.Errorf("held past message_bytes: %d", st)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("refused after %v", d)
	}
	if st, answer := c.postRaw("count=1&ofs=1&req0___data__=" + url.QueryEscape(addDoc(2, "aruma-ref-wc/a"))); st != http.StatusOK {
		t.Fatalf("next: %d %s", st, answer)
	}
	if st, answer := c.postRaw("count=1&ofs=2&req0___data__=" + url.QueryEscape(removeTarget(2))); st != http.StatusOK {
		t.Fatalf("gap: %d %s", st, answer)
	}
	if got := b.targets(t, time.Second); !slices.Equal(got, []string{"ADD[2]", "ADD[3]"}) {
		t.Errorf("targets %v", got)
	}
}

// docs is n documents' paths.
func docs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "aruma-ref-wc/" + prefix + strconv.Itoa(i)
	}
	return out
}

// A backchannel that another replaced numbers no noop after it: the new one would wait for data
// to send its own, and the client's watchdog would end it first.
func TestReplacedBackchannelNoop(t *testing.T) {
	x := newHarness(t, options{timing: webchannel.Timing{Noop: 2 * time.Millisecond}})
	c, _ := handshake(t, x.url, admin(t, x), addDoc(1, "aruma-ref-wc/a"))
	b := c.back("0")
	b.want(t, "ADD[1]")
	for i := range 40 {
		b = c.back("0")
		deadline := time.After(500 * time.Millisecond)
	wait:
		for {
			select {
			case a, ok := <-b.arrays:
				if !ok {
					t.Fatalf("backchannel %d ended: %v", i, b.err)
				}
				c.aid = a.id
				if string(a.payload) == `["noop"]` {
					break wait
				}
			case <-deadline:
				t.Fatalf("backchannel %d: no noop", i)
			}
		}
	}
}
