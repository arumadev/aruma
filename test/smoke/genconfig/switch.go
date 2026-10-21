package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// switchListener is the server's listener with an off switch, for the offline scenarios of the
// Realtime Database end-to-end tests (test/e2e): down closes the listening socket and every
// connection the server accepted, WebSockets and long polls included, so clients see the server
// go away and new connections are refused; up listens on the same address again. The database
// and the server's state stay as they were, as after a restart.
type switchListener struct {
	addr  string
	conns chan net.Conn
	done  chan struct{}

	mu     sync.Mutex
	ln     net.Listener // nil while down
	live   map[*trackedConn]struct{}
	closed bool
}

func newSwitchListener(addr string) (*switchListener, error) {
	l := &switchListener{conns: make(chan net.Conn), done: make(chan struct{}), live: map[*trackedConn]struct{}{}}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	// Up again on the port the system picked, if it was 0.
	l.addr = ln.Addr().String()
	l.ln = ln
	go l.accept(ln)
	return l, nil
}

// accept hands ln's connections to Accept until ln is closed.
func (l *switchListener) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		t := &trackedConn{Conn: c, l: l}
		l.mu.Lock()
		if l.ln != ln {
			// Accepted just as the switch went down.
			l.mu.Unlock()
			_ = c.Close()
			continue
		}
		l.live[t] = struct{}{}
		l.mu.Unlock()
		select {
		case l.conns <- t:
		case <-l.done:
			_ = c.Close()
			return
		}
	}
}

func (l *switchListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *switchListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		close(l.done)
		if l.ln != nil {
			_ = l.ln.Close()
		}
	}
	return nil
}

func (l *switchListener) Addr() net.Addr {
	a, _ := net.ResolveTCPAddr("tcp", l.addr)
	return a
}

// down stops listening and closes every open connection. It reports how many it closed.
func (l *switchListener) down() int {
	l.mu.Lock()
	ln, live := l.ln, l.live
	l.ln, l.live = nil, map[*trackedConn]struct{}{}
	l.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for c := range live {
		_ = c.Conn.Close()
	}
	return len(live)
}

// up listens again on the same address. The port can stay busy for a moment after down.
func (l *switchListener) up() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return net.ErrClosed
	}
	if l.ln != nil {
		return nil
	}
	var err error
	for range 50 {
		var ln net.Listener
		if ln, err = net.Listen("tcp", l.addr); err == nil {
			l.ln = ln
			go l.accept(ln)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// trackedConn leaves the listener's set of live connections when it is closed.
type trackedConn struct {
	net.Conn
	l    *switchListener
	once sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.l.mu.Lock()
		delete(c.l.live, c)
		c.l.mu.Unlock()
	})
	return c.Conn.Close()
}

// serveSwitch serves the switch's control on addr, for the tests: POST /down and POST /up.
// It returns the control's base URL.
func serveSwitch(addr string, l *switchListener) (string, func(), error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /down", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "down, %d connections closed\n", l.down())
	})
	mux.HandleFunc("POST /up", func(w http.ResponseWriter, r *http.Request) {
		if err := l.up(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintln(w, "up")
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print("switch: ", err)
		}
	}()
	return "http://" + ln.Addr().String(), func() { _ = srv.Close() }, nil
}
