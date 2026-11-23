package storetest

import (
	"net"
	"net/url"
	"sync"
	"testing"
)

// FreezeProxy stands between a server and Postgres and can freeze: while frozen it keeps every
// connection open and accepts new ones, but forwards nothing either way, as a paused Postgres
// container, a stalled disk or a network partition does to its clients (the kernel still
// acknowledges, so neither TCP nor the server notice). Thaw forwards again what was held.
type FreezeProxy struct {
	ln       net.Listener
	upstream string

	mu     sync.Mutex
	cond   *sync.Cond
	frozen bool
	closed bool
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
}

// NewFreezeProxy starts a proxy in front of the database at databaseURL, and returns it with
// the same URL pointed at the proxy. The proxy closes when the test ends.
func NewFreezeProxy(t testing.TB, databaseURL string) (*FreezeProxy, string) {
	t.Helper()
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &FreezeProxy{ln: ln, upstream: u.Host, conns: map[net.Conn]struct{}{}}
	p.cond = sync.NewCond(&p.mu)
	u.Host = ln.Addr().String()
	p.wg.Add(1)
	go p.accept()
	t.Cleanup(p.Close)
	return p, u.String()
}

// Freeze stops forwarding.
func (p *FreezeProxy) Freeze() {
	p.mu.Lock()
	p.frozen = true
	p.mu.Unlock()
}

// Thaw forwards again.
func (p *FreezeProxy) Thaw() {
	p.mu.Lock()
	p.frozen = false
	p.mu.Unlock()
	p.cond.Broadcast()
}

// Conns returns how many client connections are open through the proxy.
func (p *FreezeProxy) Conns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns) / 2
}

// Close closes the listener and every connection.
func (p *FreezeProxy) Close() {
	p.mu.Lock()
	p.closed, p.frozen = true, false
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.cond.Broadcast()
	_ = p.ln.Close()
	p.wg.Wait()
}

func (p *FreezeProxy) accept() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", p.upstream)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = c.Close()
			_ = up.Close()
			return
		}
		p.conns[c], p.conns[up] = struct{}{}, struct{}{}
		p.mu.Unlock()
		p.wg.Add(2)
		go p.pump(up, c)
		go p.pump(c, up)
	}
}

// pump copies from src to dst, holding each chunk while the proxy is frozen. When either side
// ends, both are closed.
func (p *FreezeProxy) pump(dst, src net.Conn) {
	defer p.wg.Done()
	defer func() {
		p.mu.Lock()
		delete(p.conns, src)
		delete(p.conns, dst)
		p.mu.Unlock()
		_ = src.Close()
		_ = dst.Close()
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			p.mu.Lock()
			for p.frozen && !p.closed {
				p.cond.Wait()
			}
			p.mu.Unlock()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
