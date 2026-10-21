package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestSwitchListenerDownAndUp(t *testing.T) {
	l, err := newSwitchListener("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	addr := l.Addr().String()

	get := func() (string, error) {
		res, err := (&http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Get("http://" + addr + "/")
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return string(b), err
	}
	if body, err := get(); err != nil || body != "ok" {
		t.Fatalf("up: %q, %v", body, err)
	}

	// A connection kept open, as a WebSocket is, is closed by down.
	open, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if _, err := io.WriteString(open, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(open)
	res, err := http.ReadResponse(r, nil)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("keep-alive request: %v, %v", res, err)
	}
	_, _ = io.ReadAll(res.Body)
	if n := l.down(); n < 1 {
		t.Fatalf("down closed %d connections, want the open one", n)
	}
	_ = open.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := r.ReadByte(); err == nil {
		t.Fatal("the open connection still answers after down")
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("a new connection was accepted while down")
	}

	if err := l.up(); err != nil {
		t.Fatal(err)
	}
	if body, err := get(); err != nil || body != "ok" {
		t.Fatalf("up again: %q, %v", body, err)
	}
}
