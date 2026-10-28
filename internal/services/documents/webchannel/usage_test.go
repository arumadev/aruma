package webchannel_test

import (
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/webchannel"
)

// TestUsage: over WebChannel, a Listen session's documents sent are reads, the session a
// connection and its target a snapshot listener, and a Write session's batches are writes, as
// over gRPC.
func TestUsage(t *testing.T) {
	x := newHarness(t, options{})
	x.commit(t, "wcu/a", 1)
	c, _ := handshake(t, x.url, admin(t, x), addDoc(2, "wcu/a", "wcu/none"))
	b := c.back("0")
	b.want(t, "ADD[2]", "change wcu/a", "delete wcu/none", "CURRENT[2]", "global")

	wurl := strings.Replace(x.url, webchannel.ListenPath, webchannel.WritePath, 1)
	wc, _ := handshake(t, wurl, admin(t, x), writeReq(""))
	wb := wc.back("0")
	hello, _ := wb.writeResponse(10 * time.Second)
	wc.send(writeReq(hello["streamToken"].(string), "wcu/c", "wcu/d"))
	if res, a := wb.writeResponse(10 * time.Second); res["commitTime"] == nil {
		t.Fatalf("write batch: %s", a.payload)
	}
	x.commit(t, "wcu/a", 2)
	b.want(t, "change wcu/a", "global")

	hs, err := x.env.Usage.Hours(t.Context(), x.env.Number, time.Now())
	if err != nil || len(hs) != 1 {
		t.Fatalf("usage: %+v %v", hs, err)
	}
	if u := hs[0]; u.Writes != 4 || u.Reads != 2 || u.ListenersPeak != 1 || u.ConnectionsPeak != 1 {
		t.Errorf("usage: %+v", u)
	}
}
